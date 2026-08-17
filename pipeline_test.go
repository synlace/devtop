package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v2"
)

// setupPipelineEnv points DEVTOP_DIR at a temp workspace and engineConfig at
// the kinds the target workflow renders, restoring both afterwards. Kinds live
// directly under DEVTOP_DIR so the DOCS/TICKETS globals match kindRoot().
func setupPipelineEnv(t *testing.T) string {
	prevDevtop := DEVTOP_DIR
	prevCfg := engineConfig
	tempDir := setupTestDirs(t)
	DEVTOP_DIR = tempDir
	for _, d := range []string{DOCS_DIR, TICKETS_DIR, THREADS_DIR, DATA_DIR} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	engineConfig = EngineConfig{
		ArtifactKinds: map[string]ArtifactKind{
			"intents":        {Path: "intents", Extension: ".mdx", AgentWritable: false},
			"documentation":  {Path: "documentation", Extension: ".mdx", AgentWritable: true},
			"requirements":   {Path: "requirements", Extension: ".mdx", AgentWritable: true},
			"decisions":      {Path: "decisions", Extension: ".mdx", AgentWritable: true},
			"open_questions": {Path: "open_questions", Extension: ".mdx", AgentWritable: true},
			"tickets":        {Path: "tickets", Extension: ".md"},
		},
		Derivation: []DerivationEdge{
			{From: "intents", To: "documentation", Transform: "describe_feature", Agent: "doc-builder", Gate: "intents.review == approved", Prompt: "Turn the intent into one feature document."},
			{From: "documentation", To: "requirements", Transform: "derive_requirements", Agent: "semantics-builder", Gate: "documentation.review == approved", Prompt: "Break the documentation into requirements."},
			{From: "documentation", To: "decisions", Transform: "derive_decisions", Agent: "semantics-builder", Gate: "documentation.review == approved"},
			{From: "documentation", To: "open_questions", Transform: "derive_open_questions", Agent: "semantics-builder", Gate: "documentation.review == approved"},
			{From: "requirements", To: "tickets", Transform: "derive_tickets", Agent: "ticket-deriver", Gate: "requirements.review == approved"},
		},
		Pipeline: PipelineConfig{Nav: &EngineNav{Label: "Work items", View: "pipeline", Order: 1}},
	}
	t.Cleanup(func() {
		DEVTOP_DIR = prevDevtop
		engineConfig = prevCfg
		os.RemoveAll(tempDir)
	})
	if err := writeEngineConfigFile(tempDir); err != nil {
		t.Fatal(err)
	}
	// Register the workspace as the default project so buildPipeline and the
	// repo-scoped handlers resolve it, as Resolve("") synthesized before
	// single mode was removed.
	registerWorkspaceRepo(t)
	return tempDir
}

// writeEngineConfigFile persists the fixture config as config.yml so
// handler-based tests (which resolve their config from the repo file, falling
// back to the bundled default) see the same kinds and edges as the globals.
func writeEngineConfigFile(dir string) error {
	data, err := yaml.Marshal(engineConfig)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.yml"), data, 0644)
}

func writeArtifact(t *testing.T, rel string, content string) {
	t.Helper()
	full := filepath.Join(DEVTOP_DIR, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func seedWorkItem(t *testing.T, id string) {
	writeArtifact(t, "intents/"+id+".mdx", "---\ntitle: \""+id+"\"\nreview: approved\n---\n\nIntent body.\n")
}

func stageOf(res PipelineResponse, kind string) []PipelineArtifact {
	if len(res.Items) == 0 {
		return nil
	}
	return res.Items[0].Stages[kind]
}

func TestBuildPipeline_LinksIntentStages(t *testing.T) {
	setupPipelineEnv(t)
	seedWorkItem(t, "INT-001")
	writeArtifact(t, "documentation/DOC-001.mdx", "---\ntitle: \"Doc\"\nwork_item: INT-001\nderived_from: intents/INT-001\nreview: approved\n---\n\nDoc body.\n")
	writeArtifact(t, "requirements/REQ-001.mdx", "---\ntitle: \"R1\"\nwork_item: INT-001\nderived_from: documentation/DOC-001\nreview: approved\n---\n\nR1.\n")
	writeArtifact(t, "requirements/REQ-002.mdx", "---\ntitle: \"R2\"\nwork_item: INT-001\nderived_from: documentation/DOC-001\nreview: approved\n---\n\nR2.\n")
	writeArtifact(t, "decisions/DEC-001.mdx", "---\ntitle: \"D1\"\nwork_item: INT-001\nderived_from: documentation/DOC-001\nreview: approved\n---\n\nD1.\n")
	writeArtifact(t, "open_questions/OQ-001.mdx", "---\ntitle: \"Q1\"\nwork_item: INT-001\nderived_from: documentation/DOC-001\nreview: approved\n---\n\nQ1.\n")
	writeArtifact(t, "tickets/001.md", "---\nid: \"001\"\ntitle: \"T1\"\nwork_item: INT-001\nreq: REQ-001\nreview: approved\n---\n\nT1.\n")

	res := buildPipeline()
	if len(res.Edges) != 5 {
		t.Fatalf("expected 5 edges, got %d", len(res.Edges))
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 root item, got %d", len(res.Items))
	}
	it := res.Items[0]
	if it.ID != "INT-001" || it.Review != "approved" {
		t.Errorf("item = %q review=%q", it.ID, it.Review)
	}
	if got := stageOf(res, "documentation"); len(got) != 1 || got[0].ID != "DOC-001" {
		t.Errorf("documentation stage = %+v", got)
	}
	if got := stageOf(res, "requirements"); len(got) != 2 {
		t.Errorf("requirements stage = %+v", got)
	}
	if got := stageOf(res, "tickets"); len(got) != 1 || got[0].ID != "001" {
		t.Errorf("tickets stage = %+v", got)
	}
	// REQ-002 has no ticket: uncovered, and the chain is complete otherwise.
	if it.Uncovered != 1 {
		t.Errorf("uncovered = %d, want 1", it.Uncovered)
	}
	if !it.Ready {
		t.Error("ready should be true: every artifact, including tickets, is approved")
	}
}

func TestBuildPipeline_StopsAtPendingStage(t *testing.T) {
	setupPipelineEnv(t)
	seedWorkItem(t, "INT-001")
	writeArtifact(t, "documentation/DOC-001.mdx", "---\ntitle: \"Doc\"\nwork_item: INT-001\nderived_from: intents/INT-001\nreview: pending\n---\n\nDoc body.\n")

	res := buildPipeline()
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(res.Items))
	}
	if res.Items[0].Ready {
		t.Error("a work item with pending artifacts must not be ready")
	}
}

func TestBuildPipeline_StaleWhenSourceNewer(t *testing.T) {
	setupPipelineEnv(t)
	seedWorkItem(t, "INT-001")
	writeArtifact(t, "documentation/DOC-001.mdx", "---\ntitle: \"Doc\"\nwork_item: INT-001\nderived_from: intents/INT-001\nreview: approved\n---\n\nDoc body.\n")
	doc := filepath.Join(DEVTOP_DIR, "documentation/DOC-001.mdx")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(doc, past, past); err != nil {
		t.Fatal(err)
	}
	// The intent file just seeded has a current mtime; the doc is older.
	res := buildPipeline()
	if !res.Items[0].Stale {
		t.Error("expected stale=true when the source is newer than the derived artifact")
	}
	if !stageOf(res, "documentation")[0].Stale {
		t.Error("expected the documentation artifact itself to be flagged stale")
	}
}

func TestEvalGate(t *testing.T) {
	setupPipelineEnv(t)
	seedWorkItem(t, "INT-001")

	// "approved" intent: the gate must pass; wrong review value must fail.
	if !evalGate("intents.review == approved", "intents", "INT-001") {
		t.Error("gate should pass for an approved intent")
	}
	// A second, unapproved intent fails closed.
	writeArtifact(t, "intents/INT-002.mdx", "---\ntitle: \"B\"\nreview: pending\n---\n\nX.\n")
	if evalGate("intents.review == approved", "intents", "INT-002") {
		t.Error("gate should fail for a pending intent")
	}
	// Wrong kind and malformed gates fail closed.
	if evalGate("documentation.review == approved", "intents", "INT-001") {
		t.Error("gate on the wrong kind must fail")
	}
	if evalGate("garbage", "intents", "INT-001") {
		t.Error("malformed gate must fail")
	}
}

func TestHandleAPIDerive_GateEnforcedBeforeModel(t *testing.T) {
	setupPipelineEnv(t)
	t.Setenv("AI_API_KEY", "")
	t.Setenv("DEVTOP_AI_ENV_FILE", filepath.Join(t.TempDir(), "no-ai"))
	// A pending intent: documentation derivation must stop at the gate.
	writeArtifact(t, "intents/INT-001.mdx", "---\ntitle: \"INT-001\"\nreview: pending\n---\n\nIntent body.\n")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/derive", handleAPIDerive)
	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/derive", strings.NewReader(`{"from":"intents","to":"documentation","slug":"INT-001"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Pending intent: the derivation gate fails before the key check.
	if rec := send(); rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for an unapproved intent, got %d", rec.Code)
	}
	// Approve the intent: the gate passes; the next failure is the missing
	// AI key (502), proving the gate did not stop it.
	writeArtifact(t, "intents/INT-001.mdx", "---\ntitle: \"INT-001\"\nreview: approved\n---\n\nIntent body.\n")
	if rec := send(); rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502 for missing key after gate pass, got %d", rec.Code)
	}
}

func TestHandleAPIDerive_UnknownEdge(t *testing.T) {
	setupPipelineEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/derive", handleAPIDerive)
	req := httptest.NewRequest("POST", "/api/derive", strings.NewReader(`{"from":"intents","to":"nope","slug":"INT-001"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown edge, got %d", rec.Code)
	}
}

func TestHandleAPIArtifactReview(t *testing.T) {
	setupPipelineEnv(t)
	seedWorkItem(t, "INT-001")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/artifacts/{kind}/{id}/review", handleAPIArtifactReview)

	post := func(id, review string) int {
		req := httptest.NewRequest("POST", "/api/artifacts/intents/"+id+"/review", strings.NewReader(`{"review":"`+review+`"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := post("INT-001", "pending"); code != http.StatusOK {
		t.Errorf("set pending expected 200, got %d", code)
	}
	if code := post("INT-001", "approved"); code != http.StatusOK {
		t.Errorf("set approved expected 200, got %d", code)
	}
	if code := post("INT-001", "bogus"); code != http.StatusBadRequest {
		t.Errorf("invalid review value expected 400, got %d", code)
	}
	if code := post("INT-999", "approved"); code != http.StatusNotFound {
		t.Errorf("missing artifact expected 404, got %d", code)
	}
	meta := readMeta(t, "intents/INT-001.mdx")
	if r, _ := meta["review"].(string); r != "approved" {
		t.Errorf("review after last write = %q, want approved", r)
	}
}

func TestHandleAPIArtifactReview_KnownKindOnly(t *testing.T) {
	setupPipelineEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/artifacts/{kind}/{id}/review", handleAPIArtifactReview)
	req := httptest.NewRequest("POST", "/api/artifacts/no-such/abc/review", strings.NewReader(`{"review":"approved"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown kind expected 404, got %d", rec.Code)
	}
}

func TestUncoveredReqIDs_Delta(t *testing.T) {
	setupPipelineEnv(t)
	seedWorkItem(t, "INT-001")
	writeArtifact(t, "requirements/REQ-001.mdx", "---\ntitle: \"R1\"\nwork_item: INT-001\nreview: approved\n---\n\nB\n")
	writeArtifact(t, "requirements/REQ-011.mdx", "---\ntitle: \"R2\"\nwork_item: INT-001\nreview: approved\n---\n\nB\n")
	writeArtifact(t, "requirements/REQ-012.mdx", "---\ntitle: \"R3\"\nwork_item: INT-001\nreview: approved\n---\n\nB\n")
	writeArtifact(t, "tickets/001.md", "---\nid: \"001\"\nreq: REQ-001\nwork_item: INT-001\n---\n\nB\n")
	missing := uncoveredReqIDs(engineConfig, defaultPaths(), "REQ-001")
	want := []string{"REQ-011", "REQ-012"}
	sort.Strings(missing)
	if strings.Join(missing, ",") != strings.Join(want, ",") {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
}

func TestUnapprovedSiblings_BlockDelta(t *testing.T) {
	setupPipelineEnv(t)
	seedWorkItem(t, "INT-001")
	writeArtifact(t, "requirements/REQ-001.mdx", "---\ntitle: \"R1\"\nwork_item: INT-001\nreview: approved\n---\n\nB\n")
	writeArtifact(t, "requirements/REQ-002.mdx", "---\ntitle: \"R2\"\nwork_item: INT-001\nreview: pending\n---\n\nB\n")
	bad := unapprovedSiblings(engineConfig, defaultPaths(), "REQ-001")
	if len(bad) != 1 || bad[0] != "REQ-002" {
		t.Fatalf("unapproved siblings = %v, want [REQ-002]", bad)
	}
}

func TestHandleDerive_TicketsCoveredShortCircuits(t *testing.T) {
	setupPipelineEnv(t)
	// setupPipelineEnv registered the workspace as the default project;
	// handleAPIDerive resolves it and reads the fixture kinds written to
	// config.yml in DEVTOP_DIR.

	seedWorkItem(t, "INT-001")
	writeArtifact(t, "requirements/REQ-001.mdx", "---\ntitle: \"R1\"\nwork_item: INT-001\nreview: approved\n---\n\nB\n")
	writeArtifact(t, "requirements/REQ-002.mdx", "---\ntitle: \"R2\"\nwork_item: INT-001\nreview: approved\n---\n\nB\n")
	writeArtifact(t, "tickets/001.md", "---\nid: \"001\"\nreq: REQ-001\nwork_item: INT-001\n---\n\nB\n")
	writeArtifact(t, "tickets/002.md", "---\nid: \"002\"\nreq: REQ-002\nwork_item: INT-001\n---\n\nB\n")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/derive", handleAPIDerive)
	req := httptest.NewRequest("POST", "/api/derive", strings.NewReader(`{"from":"requirements","to":"tickets","slug":"REQ-001"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("covered derive = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "All requirements already have tickets") {
		t.Errorf("missing covered note: %s", rec.Body.String())
	}
}

func TestHandleDerive_TicketsBlockedByUnapprovedSibling(t *testing.T) {
	setupPipelineEnv(t)
	seedWorkItem(t, "INT-001")
	writeArtifact(t, "requirements/REQ-001.mdx", "---\ntitle: \"R1\"\nwork_item: INT-001\nreview: approved\n---\n\nB\n")
	writeArtifact(t, "requirements/REQ-002.mdx", "---\ntitle: \"R2\"\nwork_item: INT-001\nreview: pending\n---\n\nB\n")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/derive", handleAPIDerive)
	req := httptest.NewRequest("POST", "/api/derive", strings.NewReader(`{"from":"requirements","to":"tickets","slug":"REQ-001"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("sibling gate expected 409, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "REQ-002") {
		t.Errorf("blocking message should name REQ-002: %s", rec.Body.String())
	}
}

func TestDeriveMessage_UsesEdgePrompt(t *testing.T) {
	setupPipelineEnv(t)
	e := engineConfig.Derivation[0]
	msg := deriveTaskMessageFor(engineConfig, e, "INT-001")
	if !strings.Contains(msg, "Turn the intent into one feature document") {
		t.Errorf("edge prompt missing from message: %s", msg)
	}
	if !strings.Contains(msg, "intents") || !strings.Contains(msg, "INT-001") {
		t.Errorf("source context missing from message: %s", msg)
	}
	// An edge without a prompt falls back to a generic instruction.
	e.Prompt = ""
	msg = deriveTaskMessageFor(engineConfig, e, "INT-001")
	if !strings.Contains(msg, "write_artifact") {
		t.Errorf("generic fallback missing from message: %s", msg)
	}
}

func TestCreateTicket_SourceField(t *testing.T) {
	setupPipelineEnv(t)
	dispatchTool("create_ticket", map[string]interface{}{
		"title": "From requirement", "description": "D", "priority": "low", "source": "requirements/REQ-001",
	})
	tickets, err := listTickets()
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 || tickets[0].Source != "requirements/REQ-001" {
		t.Errorf("ticket source not persisted: %+v", tickets)
	}
}

func TestPipelineRow_UncoveredDelta(t *testing.T) {
	setupPipelineEnv(t)
	seedWorkItem(t, "INT-001")
	writeArtifact(t, "requirements/REQ-001.mdx", "---\ntitle: \"R1\"\nwork_item: INT-001\nreview: approved\n---\n\nB\n")
	writeArtifact(t, "requirements/REQ-002.mdx", "---\ntitle: \"R2\"\nwork_item: INT-001\nreview: approved\n---\n\nB\n")
	writeArtifact(t, "tickets/001.md", "---\nid: \"001\"\nreq: REQ-001\nwork_item: INT-001\n---\n\nB\n")
	res := buildPipeline()
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	if res.Items[0].Uncovered != 1 {
		t.Errorf("uncovered = %d, want 1", res.Items[0].Uncovered)
	}
}

func TestHandleAPIIntentCreate(t *testing.T) {
	setupPipelineEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/intents", handleAPIIntentCreate)

	post := func(title string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/intents", strings.NewReader(`{"title":"`+title+`"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := post("A calculator that remembers")
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["id"] != "INT-001" || out["review"] != "pending" {
		t.Errorf("created intent = %+v, want INT-001 pending", out)
	}
	meta := readMeta(t, "intents/INT-001.mdx")
	if m, _ := meta["review"].(string); m != "pending" {
		t.Errorf("review = %q, want pending", m)
	}

	// The next seed advances the id and never collides.
	rec2 := post("Another")
	if rec2.Code != http.StatusOK {
		t.Fatalf("second create status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	var out2 map[string]interface{}
	_ = json.Unmarshal(rec2.Body.Bytes(), &out2)
	if out2["id"] != "INT-002" {
		t.Errorf("second id = %v, want INT-002", out2["id"])
	}

	// A blank title is rejected.
	if rec := post("   "); rec.Code != http.StatusBadRequest {
		t.Errorf("blank title expected 400, got %d", rec.Code)
	}
}

func TestAPIPipeline_Empty(t *testing.T) {
	setupPipelineEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/pipeline", handleAPIPipeline)
	req := httptest.NewRequest("GET", "/api/pipeline", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var res PipelineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Items == nil {
		t.Error("items must be [] not null")
	}
	if len(res.Items) != 0 {
		t.Errorf("expected empty items, got %d", len(res.Items))
	}
}
