package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupClassifyEnv provides the docs -> prds classifier fixture the prospect
// machinery still supports. The default workflow dropped the classifier; these
// tests exercise the generic classifier code, so they define their own kinds.
func setupClassifyEnv(t *testing.T) string {
	prevDevtop := DEVTOP_DIR
	prevCfg := engineConfig
	tempDir := setupTestDirs(t)
	DEVTOP_DIR = tempDir
	engineConfig = EngineConfig{
		ArtifactKinds: map[string]ArtifactKind{
			"docs":    {Path: "docs", Extension: ".mdx", AgentWritable: true},
			"prds":    {Path: "prds", Extension: ".mdx", AgentWritable: true},
			"tickets": {Path: "tickets", Extension: ".md"},
		},
		Derivation: []DerivationEdge{
			{From: "docs", To: "prds", Transform: "breakdown", Agent: "prd-builder", Classifier: "classify-doc"},
			{From: "prds", To: "tickets", Transform: "derive_tickets", Agent: "ticket-deriver"},
		},
		Pipeline: PipelineConfig{},
	}
	t.Cleanup(func() {
		DEVTOP_DIR = prevDevtop
		engineConfig = prevCfg
		os.RemoveAll(tempDir)
	})
	if err := writeEngineConfigFile(tempDir); err != nil {
		t.Fatal(err)
	}
	registerWorkspaceRepo(t)
	return tempDir
}

func TestDocProspect_Parse(t *testing.T) {
	setupClassifyEnv(t)
	tests := []struct {
		name   string
		front  string
		want   string
		wantBy string
	}{
		{"unassessed", "---\ntitle: \"X\"\n---\n\nBody.\n", "", ""},
		{"model eligible", "---\ntitle: \"X\"\nderive_prospects:\n  prds: eligible\nprospect_by: model\n---\n\nBody.\n", "eligible", "model"},
		{"user not-eligible", "---\ntitle: \"X\"\nderive_prospects:\n  prds: not-eligible\nprospect_by: user\n---\n\nBody.\n", "not-eligible", "user"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writeArtifact(t, "docs/d.mdx", tc.front)
			meta := readMeta(t, "docs/d.mdx")
			v, by := docProspect(meta, "prds")
			if v != tc.want || by != tc.wantBy {
				t.Errorf("docProspect = (%q, %q), want (%q, %q)", v, by, tc.want, tc.wantBy)
			}
		})
	}
}

func readMeta(t *testing.T, rel string) map[string]interface{} {
	t.Helper()
	meta, _, err := readFrontmatterFile(joinPath(DEVTOP_DIR, rel))
	if err != nil {
		t.Fatalf("read frontmatter: %v", err)
	}
	return meta
}

// testRepo returns a repo over the current globals, as registry Resolve("")
// did before single mode was removed, for tests that exercise repo-scoped
// logic.
func testRepo() *Repo {
	return &Repo{
		Name:  repoNameForRoot(DEVTOP_DIR),
		Root:  filepath.Dir(DEVTOP_DIR),
		Dir:   DEVTOP_DIR,
		paths: defaultPaths(),
	}
}

func TestAssessmentTarget(t *testing.T) {
	setupClassifyEnv(t)
	writeArtifact(t, "docs/architecture.mdx", "---\ntitle: A\n---\n\nB\n")
	repo := testRepo()

	k, s, ok := assessmentTarget(repo, "write_artifact", map[string]interface{}{"kind": "docs", "id": "architecture"})
	if !ok || k != "docs" || s != "architecture" {
		t.Errorf("write_artifact target = (%q, %q, %v), want (docs, architecture, true)", k, s, ok)
	}

	k, s, ok = assessmentTarget(repo, "write_doc", map[string]interface{}{"path": "architecture"})
	if !ok || k != "docs" || s != "architecture" {
		t.Errorf("write_doc target = (%q, %q, %v), want (docs, architecture, true)", k, s, ok)
	}

	k, s, ok = assessmentTarget(repo, "write_artifact", map[string]interface{}{"kind": "docs", "id": "index"})
	if !ok || s != "index" {
		t.Errorf("write_artifact index target = (%q, %q, %v)", k, s, ok)
	}

	// prds has no classifier-bound outgoing edge.
	if _, _, ok := assessmentTarget(repo, "write_artifact", map[string]interface{}{"kind": "prds", "id": "x"}); ok {
		t.Error("prds write must not trigger assessment")
	}
	// Non-write and unknown tools never trigger.
	if _, _, ok := assessmentTarget(repo, "git_commit", map[string]interface{}{}); ok {
		t.Error("git_commit must not trigger assessment")
	}
}

func TestGetAPIConfig_VolumeAuthority(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVTOP_AI_ENV_FILE", filepath.Join(dir, "ai.env"))
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_BASE_URL", "")
	t.Setenv("AI_MODEL", "")

	if cfg := getAPIConfig(); cfg.HasKey {
		t.Error("no key expected without a volume file or env")
	}

	// The persisted volume file is authoritative when it has a key.
	writeErr := os.WriteFile(filepath.Join(dir, "ai.env"), []byte("AI_API_KEY=sk-vol\nAI_MODEL=\"m2\"\n"), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	cfg := getAPIConfig()
	if !cfg.HasKey || cfg.APIKey != "sk-vol" || cfg.Model != "m2" {
		t.Errorf("volume authority not applied: %+v", cfg)
	}

	// "not-needed" means deliberately no key: fall through to env.
	writeErr = os.WriteFile(filepath.Join(dir, "ai.env"), []byte("AI_API_KEY=not-needed\n"), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	t.Setenv("AI_API_KEY", "sk-env")
	if cfg := getAPIConfig(); !cfg.HasKey || cfg.APIKey != "sk-env" {
		t.Errorf("not-needed should fall through to env: %+v", cfg)
	}

	// A file without a key still falls through to env for the key.
	writeErr = os.WriteFile(filepath.Join(dir, "ai.env"), []byte("AI_BASE_URL=https://example.com/v1\n"), 0600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if cfg := getAPIConfig(); !cfg.HasKey || cfg.APIKey != "sk-env" {
		t.Errorf("env fallback for key: %+v", cfg)
	}
	if cfg := getAPIConfig(); cfg.BaseURL != "https://example.com/v1" {
		t.Errorf("volume base URL not applied: %q", cfg.BaseURL)
	}
}

func joinPath(dir, rel string) string {
	return strings.TrimSuffix(dir, "/") + "/" + rel
}

func TestHandleProspect_VerdictFlip(t *testing.T) {
	setupClassifyEnv(t)
	writeArtifact(t, "docs/reference/deployment.mdx", "---\ntitle: \"Deployment\"\n---\n\n# Deployment\n\nOps doc.\n")

	req := httptest.NewRequest(http.MethodPost, "/api/pipeline/prospect",
		strings.NewReader(`{"kind":"docs","slug":"reference/deployment","verdict":"eligible"}`))
	rr := httptest.NewRecorder()
	handleAPIProspect(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["prospect"] != "eligible" || out["prospect_by"] != "user" {
		t.Errorf("response = %+v", out)
	}
	meta := readMeta(t, "docs/reference/deployment.mdx")
	v, by := docProspect(meta, "prds")
	if v != "eligible" || by != "user" {
		t.Errorf("persisted prospect = (%q, %q)", v, by)
	}
}

func TestHandleProspect_VerdictRejectsUnsafeInput(t *testing.T) {
	setupClassifyEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/api/pipeline/prospect",
		strings.NewReader(`{"kind":"docs","slug":"../evil","verdict":"eligible"}`))
	rr := httptest.NewRecorder()
	handleAPIProspect(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("unsafe slug status = %d", rr.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/pipeline/prospect",
		strings.NewReader(`{"kind":"docs","slug":"a","verdict":"maybe"}`))
	rr = httptest.NewRecorder()
	handleAPIProspect(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad verdict status = %d", rr.Code)
	}
}

func TestHandleProspectClassify_UserVerdictWins(t *testing.T) {
	setupClassifyEnv(t)
	engineConfig.Derivation[0].Classifier = "classify-doc"
	writeArtifact(t, "docs/reference/deployment.mdx", "---\ntitle: \"Deployment\"\nderive_prospects:\n  prds: not-eligible\nprospect_by: user\n---\n\nBody.\n")

	req := httptest.NewRequest(http.MethodPost, "/api/pipeline/prospect/classify",
		strings.NewReader(`{"kind":"docs","slug":"reference/deployment"}`))
	rr := httptest.NewRecorder()
	handleAPIProspectClassify(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("classify over user verdict status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandleProspectClassify_NoClassifierBound(t *testing.T) {
	setupClassifyEnv(t)
	writeArtifact(t, "prds/x/index.mdx", "---\ntitle: \"X\"\nstatus: \"draft\"\n---\n\nBody.\n")
	req := httptest.NewRequest(http.MethodPost, "/api/pipeline/prospect/classify",
		strings.NewReader(`{"kind":"prds","slug":"x"}`))
	rr := httptest.NewRecorder()
	handleAPIProspectClassify(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("no-classifier status = %d", rr.Code)
	}
}

func TestHandleDerive_ProspectGate(t *testing.T) {
	setupClassifyEnv(t)
	engineConfig.Derivation[0].Classifier = "classify-doc"
	t.Setenv("AI_API_KEY", "")
	t.Setenv("DEVTOP_AI_ENV_FILE", filepath.Join(t.TempDir(), "no-ai"))
	writeArtifact(t, "docs/reference/deployment.mdx", "---\ntitle: \"Deployment\"\nderive_prospects:\n  prds: not-eligible\nprospect_by: model\n---\n\nBody.\n")

	req := httptest.NewRequest(http.MethodPost, "/api/derive",
		strings.NewReader(`{"from":"docs","to":"prds","slug":"reference/deployment"}`))
	rr := httptest.NewRecorder()
	handleAPIDerive(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("ineligible derive status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "not eligible") {
		t.Errorf("missing gate message: %s", rr.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/pipeline/prospect",
		strings.NewReader(`{"kind":"docs","slug":"reference/deployment","verdict":"eligible"}`))
	rr2 := httptest.NewRecorder()
	handleAPIProspect(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("flip status = %d, body = %s", rr2.Code, rr2.Body.String())
	}

	// Eligible now: the gate passes and the handler reaches the API key
	// check (502 without a key), never running a model.
	req3 := httptest.NewRequest(http.MethodPost, "/api/derive",
		strings.NewReader(`{"from":"docs","to":"prds","slug":"reference/deployment"}`))
	rr3 := httptest.NewRecorder()
	handleAPIDerive(rr3, req3)
	if rr3.Code != http.StatusBadGateway {
		t.Errorf("eligible derive status = %d, body = %s", rr3.Code, rr3.Body.String())
	}
}

// TestWriteArtifact_ResolvesExistingDirectFile: a classify write to an id with
// an existing direct file must land in place, not at <id>/index.mdx. This is
// the "doc stayed unassessed" bug: the agent wrote, the pipeline read elsewhere.
func TestWriteArtifact_ResolvesExistingDirectFile(t *testing.T) {
	setupClassifyEnv(t)
	writeArtifact(t, "docs/roadmap.mdx", "---\ntitle: \"Roadmap\"\n---\n\n# Roadmap\n\nV1.\n")
	res := dispatchTool("write_artifact", map[string]interface{}{
		"kind": "docs", "id": "roadmap",
		"content": "---\ntitle: \"Roadmap\"\nderive_prospects:\n  prds: eligible\nprospect_by: model\n---\n\n# Roadmap\n\nV2.\n",
	})
	if strings.Contains(res, "Error") {
		t.Fatalf("write_artifact failed: %s", res)
	}
	got, err := os.ReadFile(joinPath(DEVTOP_DIR, "docs/roadmap.mdx"))
	if err != nil {
		t.Fatalf("direct file missing: %v", err)
	}
	if !strings.Contains(string(got), "prds: eligible") {
		t.Errorf("direct file not updated:\n%s", got)
	}
	if _, err := os.Stat(joinPath(DEVTOP_DIR, "docs/roadmap/index.mdx")); err == nil {
		t.Error("index.mdx must not be created when the direct file exists")
	}
	// The classify output is parseable and un-gates the derive handler.
	meta := readMeta(t, "docs/roadmap.mdx")
	v, by := docProspect(meta, "prds")
	if v != "eligible" || by != "model" {
		t.Errorf("prospect = (%q, %q), want (eligible, model)", v, by)
	}
}

// TestWriteArtifact_ResolvesExistingIndexFile: an id with an existing
// <id>/index.mdx updates it in place and never creates a sibling direct file.
func TestWriteArtifact_ResolvesExistingIndexFile(t *testing.T) {
	setupClassifyEnv(t)
	writeArtifact(t, "docs/guides/usage/index.mdx", "---\ntitle: \"Usage\"\n---\n\nV1.\n")
	res := dispatchTool("write_artifact", map[string]interface{}{
		"kind": "docs", "id": "guides/usage",
		"content": "---\ntitle: \"Usage\"\n---\n\nV2.\n",
	})
	if strings.Contains(res, "Error") {
		t.Fatalf("write_artifact failed: %s", res)
	}
	if _, err := os.Stat(joinPath(DEVTOP_DIR, "docs/guides/usage.mdx")); err == nil {
		t.Error("sibling direct file must not be created when the index exists")
	}
	got, err := os.ReadFile(joinPath(DEVTOP_DIR, "docs/guides/usage/index.mdx"))
	if err != nil {
		t.Fatalf("index file missing: %v", err)
	}
	if !strings.Contains(string(got), "V2.") {
		t.Errorf("index not updated:\n%s", got)
	}
}

// TestWriteArtifact_CreatesByConvention: with no existing file, creation keeps
// the placement convention (plain id -> <id>/index.mdx) — and the engine mints
// the id, ignoring the caller's id for a file that does not exist.
func TestWriteArtifact_CreatesByConvention(t *testing.T) {
	setupClassifyEnv(t)
	res := dispatchTool("write_artifact", map[string]interface{}{
		"kind": "docs", "id": "new-page", "content": "---\ntitle: \"New\"\n---\n\nBody.\n",
	})
	if strings.Contains(res, "Error") {
		t.Fatalf("write_artifact failed: %s", res)
	}
	if strings.Contains(res, "id=new-page") {
		t.Errorf("supplied id must be ignored for a new file, got: %s", res)
	}
	if _, err := os.Stat(joinPath(DEVTOP_DIR, "docs/1/index.mdx")); err != nil {
		t.Errorf("minted convention index missing: %v", err)
	}
	if _, err := os.Stat(joinPath(DEVTOP_DIR, "docs/new-page")); err == nil {
		t.Error("caller-supplied id must not be honored for a new file")
	}
}

// TestArtifactToolPath_MatchesExistingFile: the permission mapper and the
// write tool must resolve to the same file (authorization == write target).
func TestArtifactToolPath_MatchesExistingFile(t *testing.T) {
	setupClassifyEnv(t)
	writeArtifact(t, "docs/roadmap.mdx", "---\ntitle: R\n---\n\nB\n")
	rel, ok := artifactToolPath(map[string]interface{}{"kind": "docs", "id": "roadmap"})
	if rel == "" {
		rel = "<empty>"
	}
	if !ok || rel != "docs/roadmap.mdx" {
		t.Errorf("artifactToolPath = (%q, %v), want (docs/roadmap.mdx, true)", rel, ok)
	}
}

// TestHandleDerive_ProspectViaWriteArtifact: after the classify agent writes an
// eligible verdict through write_artifact, the derive gate passes (reaches the
// API-key check with no key). This is the contract that a successful classify
// "leaves the doc no longer unassessed".
func TestHandleDerive_ProspectViaWriteArtifact(t *testing.T) {
	setupClassifyEnv(t)
	engineConfig.Derivation[0].Classifier = "classify-doc"
	t.Setenv("AI_API_KEY", "")
	t.Setenv("DEVTOP_AI_ENV_FILE", filepath.Join(t.TempDir(), "no-ai"))
	writeArtifact(t, "docs/architecture.mdx", "---\ntitle: \"Architecture\"\n---\n\nBody.\n")
	res := dispatchTool("write_artifact", map[string]interface{}{
		"kind": "docs", "id": "architecture",
		"content": "---\ntitle: \"Architecture\"\nderive_prospects:\n  prds: eligible\nprospect_by: model\n---\n\nBody.\n",
	})
	if strings.Contains(res, "Error") {
		t.Fatalf("write_artifact failed: %s", res)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/derive",
		strings.NewReader(`{"from":"docs","to":"prds","slug":"architecture"}`))
	rr := httptest.NewRecorder()
	handleAPIDerive(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("derive after agent write = %d (want 502: gate passed, no key)", rr.Code)
	}
}

// Task message pins the target: writing anything else is an error.
func TestClassifyTaskMessage_PinsTarget(t *testing.T) {
	setupClassifyEnv(t)
	writeArtifact(t, "docs/architecture/agent-engine.mdx", "---\ntitle: \"AE\"\n---\n\nBody.\n")
	edge := engineConfig.Derivation[0]
	msg := classifyTaskMessage(edge, "architecture/agent-engine")
	for _, want := range []string{
		".devtop/docs/architecture/agent-engine.mdx",
		"The ONLY file you may write is",
		"kind \"docs\"",
		"top-level field prospect_by: model",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("classify task missing %q:\n%s", want, msg)
		}
	}
}

// classifyResult must fail closed: verdict absent, or verdict with a missing
// or nested prospect_by, counts as "not written".
func TestClassifyResult_FailClosed(t *testing.T) {
	setupClassifyEnv(t)
	edge := engineConfig.Derivation[0]

	// Untouched doc: no verdict at all.
	writeArtifact(t, "docs/a.mdx", "---\ntitle: \"A\"\n---\n\nBody.\n")
	if _, _, written := classifyResult("docs", "a", edge); written {
		t.Error("untouched doc must report written=false")
	}

	// Malformed model write: prospect_by nested inside derive_prospects.
	writeArtifact(t, "docs/b.mdx", "---\ntitle: \"B\"\nderive_prospects:\n  prds: not-eligible\n  prospect_by: model\n---\n\nBody.\n")
	v, by, written := classifyResult("docs", "b", edge)
	if v != "not-eligible" || by != "" || written {
		t.Errorf("nested prospect_by = (%q, %q, %v), want (not-eligible, \"\", false)", v, by, written)
	}

	// Correct model write: verdict + top-level prospect_by: model.
	writeArtifact(t, "docs/c.mdx", "---\ntitle: \"C\"\nderive_prospects:\n  prds: eligible\nprospect_by: model\n---\n\nBody.\n")
	v, by, written = classifyResult("docs", "c", edge)
	if v != "eligible" || by != "model" || !written {
		t.Errorf("valid model write = (%q, %q, %v), want (eligible, model, true)", v, by, written)
	}
}
