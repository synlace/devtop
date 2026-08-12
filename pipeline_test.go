package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupPipelineEnv points DEVTOP_DIR at a temp workspace and engineConfig at the
// kinds the pipeline renders, restoring both afterwards. Kinds live directly
// under DEVTOP_DIR so the DOCS/TICKETS globals match kindRoot().
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
			"docs":    {Path: "docs", Extension: ".mdx", AgentWritable: true},
			"prds":    {Path: "prds", Extension: ".mdx", AgentWritable: true},
			"tickets": {Path: "tickets", Extension: ".md"},
		},
		Derivation: []DerivationEdge{
			{From: "docs", To: "prds", Transform: "breakdown", Agent: "prd-builder"},
			{From: "prds", To: "tickets", Transform: "derive_tickets", Agent: "ticket-deriver", Gate: "prds.status == approved"},
		},
		Pipeline: PipelineConfig{Nav: &EngineNav{Label: "Pipeline", View: "pipeline", Order: 4}},
	}
	t.Cleanup(func() {
		DEVTOP_DIR = prevDevtop
		engineConfig = prevCfg
		os.RemoveAll(tempDir)
	})
	return tempDir
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

func seedPipeline(t *testing.T) {
	writeArtifact(t, "docs/architecture.mdx", "---\ntitle: \"Architecture\"\nsummary: \"How the agent works.\"\n---\n\n# Architecture\n\nDetail.\n")
	writeArtifact(t, "prds/architecture/index.mdx", "---\ntitle: \"Architecture PRD\"\nstatus: \"reviewing\"\n---\n\nPRD body.\n")
	writeArtifact(t, "tickets/001.md", "---\nid: \"001\"\ntitle: \"Do the thing\"\nstatus: \"open\"\npriority: \"high\"\nassignee: \"\"\ncreated: \"2026-08-10\"\nsource: \"prds/architecture\"\n---\n\nBody.\n")
	writeArtifact(t, "tickets/002.md", "---\nid: \"002\"\ntitle: \"Other\"\nstatus: \"open\"\npriority: \"low\"\nassignee: \"\"\ncreated: \"2026-08-10\"\nsource: \"\"\n---\n\nBody.\n")
}

func TestBuildPipeline_LinksDocPRDTickets(t *testing.T) {
	setupPipelineEnv(t)
	seedPipeline(t)

	res := buildPipeline()
	if len(res.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(res.Edges))
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 root item, got %d", len(res.Items))
	}
	it := res.Items[0]
	if it.Slug != "architecture" {
		t.Errorf("slug = %q", it.Slug)
	}
	if it.Summary != "How the agent works." {
		t.Errorf("summary = %q", it.Summary)
	}
	if it.PRD == nil || it.PRD.Status != "reviewing" || it.PRD.Slug != "architecture" {
		t.Errorf("prd = %+v", it.PRD)
	}
	if len(it.Tickets) != 1 || it.Tickets[0].ID != "001" {
		t.Errorf("tickets = %+v", it.Tickets)
	}
}

func TestBuildPipeline_StaleWhenSourceNewer(t *testing.T) {
	setupPipelineEnv(t)
	seedPipeline(t)
	prd := filepath.Join(DEVTOP_DIR, "prds/architecture/index.mdx")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(prd, past, past); err != nil {
		t.Fatal(err)
	}
	// Docs file just seeded has a current mtime; the PRD is older.
	if res := buildPipeline(); !res.Items[0].Stale {
		t.Error("expected stale=true when the source is newer than the PRD")
	}
}

func TestEvalGate(t *testing.T) {
	setupPipelineEnv(t)
	seedPipeline(t)

	// "reviewing" PRD: the approve gate must fail.
	if evalGate("prds.status == approved", "prds", "architecture") {
		t.Error("gate should fail for a reviewing PRD")
	}
	// Wrong kind and malformed gates fail closed.
	if evalGate("docs.status == approved", "prds", "architecture") {
		t.Error("gate on the wrong kind must fail")
	}
	if evalGate("garbage", "prds", "architecture") {
		t.Error("malformed gate must fail")
	}
	// Set approved and recheck.
	path, _ := resolveArtifactFile("prds", "architecture")
	meta, body, err := readFrontmatterFile(path)
	if err != nil {
		t.Fatal(err)
	}
	meta["status"] = "approved"
	writeArtifact(t, "prds/architecture/index.mdx", string(composeFrontmatter(meta, string(body))))
	if !evalGate("prds.status == approved", "prds", "architecture") {
		t.Error("gate should pass for an approved PRD")
	}
}

func TestHandleAPIDerive_GateEnforcedBeforeModel(t *testing.T) {
	setupPipelineEnv(t)
	seedPipeline(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/derive", handleAPIDerive)

	send := func(prd string) *httptest.ResponseRecorder {
		payload := strings.NewReader(`{"from":"prds","to":"tickets","slug":"architecture"}`)
		req := httptest.NewRequest("POST", "/api/derive", payload)
		// Force the PRD status here for the second call.
		if prd == "approved" {
			path, _ := resolveArtifactFile("prds", "architecture")
			meta, b, _ := readFrontmatterFile(path)
			meta["status"] = "approved"
			writeArtifact(t, "prds/architecture/index.mdx", string(composeFrontmatter(meta, string(b))))
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Reviewing PRD: blocked at the gate, before the key check.
	if rec := send("reviewing"); rec.Code != http.StatusConflict {
		t.Errorf("expected 409 for unmet gate, got %d", rec.Code)
	}

	// Approved PRD: gate passes; next failure is the missing AI key (502),
	// proving the gate did not stop it.
	if rec := send("approved"); rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502 for missing key after gate pass, got %d", rec.Code)
	}
}

func TestHandleAPIDerive_UnknownEdge(t *testing.T) {
	setupPipelineEnv(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/derive", handleAPIDerive)
	req := httptest.NewRequest("POST", "/api/derive", strings.NewReader(`{"from":"docs","to":"nope","slug":"x"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown edge, got %d", rec.Code)
	}
}

func TestHandleAPIPRDStatus_StateMachine(t *testing.T) {
	setupPipelineEnv(t)
	seedPipeline(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/pipeline/prds/{slug}/status", handleAPIPRDStatus)

	post := func(status string) int {
		req := httptest.NewRequest("POST", "/api/pipeline/prds/architecture/status", strings.NewReader(`{"status":"`+status+`"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}

	// seeding leaves the PRD "reviewing": draft is skipped.
	if code := post("approved"); code != http.StatusOK {
		t.Errorf("reviewing -> approved expected 200, got %d", code)
	}
	// Back to draft, then forward again.
	if code := post("draft"); code != http.StatusOK {
		t.Errorf("approved -> draft expected 200, got %d", code)
	}
	if code := post("approved"); code != http.StatusConflict {
		t.Errorf("draft -> approved expected 409, got %d", code)
	}
	if code := post("reviewing"); code != http.StatusOK {
		t.Errorf("draft -> reviewing expected 200, got %d", code)
	}
}

func TestHandleAPIPRDStatus_NestedSlug(t *testing.T) {
	setupPipelineEnv(t)
	writeArtifact(t, "prds/deep/nested/index.mdx", "---\ntitle: \"Deep\"\nstatus: \"draft\"\n---\n\nBody.\n")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/pipeline/prds/{slug}/status", handleAPIPRDStatus)

	// The client percent-encodes nested slugs; ServeMux matches them as one
	// segment and decodes the value back to a slash path.
	req := httptest.NewRequest("POST", "/api/pipeline/prds/deep%2Fnested/status", strings.NewReader(`{"status":"reviewing"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("nested slug status = %d, body = %s", rec.Code, rec.Body.String())
	}
	meta := readMeta(t, "prds/deep/nested/index.mdx")
	if s, _ := meta["status"].(string); s != "reviewing" {
		t.Errorf("status after set = %q, want reviewing", s)
	}
}

func TestPrdRequirementIDs(t *testing.T) {
	setupPipelineEnv(t)
	writeArtifact(t, "prds/x/index.mdx", "---\ntitle: X\nrequirements:\n  - id: REQ-001\n  - id: REQ-002\n---\n\n## REQ-003 — Cover\n\nBody.\n")
	ids := prdRequirementIDs(engineConfig, defaultPaths(), "x")
	want := []string{"REQ-001", "REQ-002", "REQ-003"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
}

func TestTicketReqAnchors_ScopeBySource(t *testing.T) {
	setupPipelineEnv(t)
	writeArtifact(t, "tickets/001.md", "---\nid: \"001\"\nreq: REQ-001\nsource: prds/x\n---\n\nB\n")
	writeArtifact(t, "tickets/002.md", "---\nid: \"002\"\nreq: REQ-099\nsource: prds/other\n---\n\nB\n")
	anchors := ticketReqAnchors(engineConfig, defaultPaths(), "prds/x")
	if !anchors["REQ-001"] {
		t.Error("REQ-001 from source prds/x not anchored")
	}
	if anchors["REQ-099"] {
		t.Error("a ticket from another source leaked into the anchors")
	}
}

func TestUncoveredReqs_Delta(t *testing.T) {
	setupPipelineEnv(t)
	writeArtifact(t, "prds/x/index.mdx", "---\ntitle: X\nrequirements:\n  - id: REQ-001\n  - id: REQ-011\n  - id: REQ-012\n---\n\nBody.\n")
	writeArtifact(t, "tickets/001.md", "---\nid: \"001\"\nreq: REQ-001\nsource: prds/x\n---\n\nB\n")
	missing := uncoveredReqs(engineConfig, defaultPaths(), "x")
	if len(missing) != 2 || missing[0] != "REQ-011" || missing[1] != "REQ-012" {
		t.Fatalf("missing = %v, want [REQ-011 REQ-012]", missing)
	}
}

func TestHandleDerive_TicketsCoveredShortCircuits(t *testing.T) {
	setupPipelineEnv(t)
	// Route the request at the synthetic repo over the temp workspace: a
	// stale registered repo from another test would skew cfg/p below.
	registry.mu.Lock()
	registry.repos = nil
	registry.byName = map[string]*Repo{}
	registry.mu.Unlock()

	writeArtifact(t, "prds/x/index.mdx", "---\ntitle: X\nstatus: approved\nrequirements:\n  - id: REQ-001\n  - id: REQ-002\n---\n\nBody.\n")
	writeArtifact(t, "tickets/001.md", "---\nid: \"001\"\nreq: REQ-001\nsource: prds/x\n---\n\nB\n")
	writeArtifact(t, "tickets/002.md", "---\nid: \"002\"\nreq: REQ-002\nsource: prds/x\n---\n\nB\n")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/derive", handleAPIDerive)
	req := httptest.NewRequest("POST", "/api/derive", strings.NewReader(`{"from":"prds","to":"tickets","slug":"x"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("covered derive = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "All requirements already have tickets") {
		t.Errorf("missing covered note: %s", rec.Body.String())
	}
}

func TestPipelineRow_UncoveredDelta(t *testing.T) {
	setupPipelineEnv(t)
	writeArtifact(t, "docs/x.mdx", "---\ntitle: X\n---\n\nBody.\n")
	writeArtifact(t, "prds/x/index.mdx", "---\ntitle: X\nrequirements:\n  - id: REQ-001\n  - id: REQ-002\n---\n\nBody.\n")
	writeArtifact(t, "tickets/001.md", "---\nid: \"001\"\nreq: REQ-001\nsource: prds/x\n---\n\nB\n")
	res := buildPipeline()
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	if res.Items[0].Uncovered != 1 {
		t.Errorf("uncovered = %d, want 1", res.Items[0].Uncovered)
	}
}

func TestCreateTicket_SourceField(t *testing.T) {
	setupPipelineEnv(t)
	dispatchTool("create_ticket", map[string]interface{}{
		"title": "From PRD", "description": "D", "priority": "low", "source": "prds/architecture",
	})
	tickets, err := listTickets()
	if err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 || tickets[0].Source != "prds/architecture" {
		t.Errorf("ticket source not persisted: %+v", tickets)
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
