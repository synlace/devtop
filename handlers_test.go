package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func initHTTPTestEnv(t *testing.T) (string, *http.ServeMux) {
	tempDir := initTestEnvironment(t)

	// Point static serving at a temp dir so SPA tests are deterministic
	// regardless of whether frontend/dist exists on disk.
	STATIC_DIR = filepath.Join(tempDir, "dist")

	// DEVTOP_DIR is the parent of the kind subdirs; point it at the temp dir
	// so engine-config materialization/loading matches main.go.
	DEVTOP_DIR = tempDir

	// Materialize + parse the engine config like main.go does.
	if _, err := ensureEngineConfig(); err != nil {
		t.Fatalf("ensureEngineConfig: %v", err)
	}
	if err := loadEngineConfig(); err != nil {
		t.Fatalf("loadEngineConfig: %v", err)
	}

	// Register the workspace as the default project so repo-scoped handlers
	// resolve, as Resolve("") synthesized before single mode was removed.
	registerWorkspaceRepo(t)

	// Set up router identical to main.go
	mux := http.NewServeMux()

	// CopilotKit proxy (registered like main.go; runtime isn't running in tests)
	mux.HandleFunc("/api/copilotkit", handleCopilotKitProxy)
	mux.HandleFunc("/api/copilotkit/", handleCopilotKitProxy)

	// API Handlers
	mux.HandleFunc("GET /api/docs", handleAPIDocs)
	mux.HandleFunc("GET /api/docs/{slug...}", handleAPIDocPage)
	mux.HandleFunc("GET /api/tickets", handleAPITickets)
	mux.HandleFunc("GET /api/tickets/{id}", handleAPITicketDetail)
	mux.HandleFunc("GET /api/revisions/docs/{slug...}", handleAPIDocRevisions)
	mux.HandleFunc("GET /api/revisions/tickets/{id}", handleAPITicketRevisions)
	mux.HandleFunc("GET /api/threads", handleAPIThreads)
	mux.HandleFunc("POST /api/threads", handleAPICreateThread)
	mux.HandleFunc("GET /api/threads/{id}", handleAPIGetThread)
	mux.HandleFunc("DELETE /api/threads/{id}", handleAPIDeleteThread)
	mux.HandleFunc("POST /api/chat/{thread_id}", handleAPIChat)
	mux.HandleFunc("GET /api/models", handleAPIModels)
	mux.HandleFunc("GET /api/config", handleAPIConfig)
	mux.HandleFunc("GET /api/engine-config", handleAPIEngineConfig)
	mux.HandleFunc("GET /api/pipeline", handleAPIPipeline)
	mux.HandleFunc("POST /api/derive", handleAPIDerive)
	mux.HandleFunc("POST /api/artifacts/{kind}/{id}/review", handleAPIArtifactReview)
	mux.HandleFunc("PUT /api/artifacts/{kind}/{id}", handleAPIArtifactUpdate)
	mux.HandleFunc("GET /api/artifacts/{kind}", handleAPIArtifacts)
	mux.HandleFunc("GET /api/artifacts/{kind}/{id...}", handleAPIArtifactDetail)
	mux.HandleFunc("POST /api/intents", handleAPIIntentCreate)
	mux.HandleFunc("POST /api/intents/{id}/publish", handleAPIWorkItemPublish)
	mux.HandleFunc("GET /api/viewstate", handleAPIGetViewState)
	mux.HandleFunc("PUT /api/viewstate", handleAPIPutViewState)
	mux.HandleFunc("GET /api/favourites", handleAPIGetFavourites)
	mux.HandleFunc("PUT /api/favourites", handleAPIPutFavourites)
	mux.HandleFunc("DELETE /api/docs/{slug...}", handleAPIDeleteDoc)

	// SPA route
	mux.HandleFunc("/{path...}", handleSPA)

	return tempDir, mux
}

func TestSPA_NotBuilt(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404 when frontend not built, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "frontend not built") {
		t.Errorf("expected 'frontend not built' message, got %q", rec.Body.String())
	}
}

func TestAPIEmptyLists(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	// initTestEnvironment seeds sample content — clear it to simulate a fresh,
	// empty repo. The list endpoints must return [] (never null) so the
	// frontend can iterate them.
	_ = os.RemoveAll(DOCS_DIR)
	_ = os.RemoveAll(TICKETS_DIR)
	_ = os.MkdirAll(DOCS_DIR, 0755)
	_ = os.MkdirAll(TICKETS_DIR, 0755)

	for _, path := range []string{"/api/docs", "/api/tickets"} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
			t.Errorf("%s: expected [], got %q", path, got)
		}
	}
}

func TestSPA_ServesIndex(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	if err := os.MkdirAll(STATIC_DIR, 0755); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(STATIC_DIR, "index.html")
	if err := os.WriteFile(index, []byte("<html><body>devtop react app</body></html>"), 0644); err != nil {
		t.Fatal(err)
	}

	// Root serves index.html
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "devtop react app") {
		t.Errorf("expected index.html body, got %q", rec.Body.String())
	}

	// Unknown non-API path falls back to index.html (SPA routing)
	req = httptest.NewRequest("GET", "/docs/architecture", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "devtop react app") {
		t.Errorf("expected SPA fallback to index.html for /docs/architecture, got %d", rec.Code)
	}
}

func TestSPA_ServesStaticAsset(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	if err := os.MkdirAll(filepath.Join(STATIC_DIR, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(STATIC_DIR, "index.html"), []byte("<!doctype html>"), 0644)
	asset := filepath.Join(STATIC_DIR, "assets", "app.js")
	_ = os.WriteFile(asset, []byte("console.log('app')"), 0644)

	req := httptest.NewRequest("GET", "/assets/app.js", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 for static asset, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "console.log") {
		t.Errorf("expected asset contents, got %q", rec.Body.String())
	}
}

func TestCopilotKitProxy_Returns502WhenRuntimeDown(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	t.Setenv("COPILOTKIT_RUNTIME_URL", "http://127.0.0.1:1")

	req := httptest.NewRequest("GET", "/api/copilotkit/ai-status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502 when runtime is down, got %d", rec.Code)
	}
}

func TestAPIRoutes_DocsList(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/api/docs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var docs []DocSlug
	if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if len(docs) < 2 {
		t.Errorf("expected at least 2 docs, got %d", len(docs))
	}
}

func TestAPIRoutes_DocPage(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/api/docs/architecture", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var doc map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if doc["title"] != "System Architecture" {
		t.Errorf("expected title 'System Architecture', got '%s'", doc["title"])
	}
	if !strings.Contains(doc["content"], "Stack: Go.") {
		t.Errorf("expected content to contain 'Stack: Go.', got '%s'", doc["content"])
	}
	// The client renders — the API must return raw Markdown, not HTML, with
	// the YAML frontmatter stripped.
	if !strings.HasPrefix(doc["content"], "# Architecture") {
		t.Errorf("expected content to start with the raw Markdown heading, got %q", doc["content"])
	}
	if strings.Contains(doc["content"], "<p>") || strings.Contains(doc["content"], "title:") {
		t.Errorf("expected raw Markdown body (no rendered HTML/frontmatter), got %q", doc["content"])
	}
}

func TestAPIRoutes_DocPageNotFound(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/api/docs/nonexistent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestAPIRoutes_FavouritesRoundtrip(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	// Empty by default
	req := httptest.NewRequest("GET", "/api/favourites", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got []string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no favourites, got %v", got)
	}

	// PUT persists (dupes and stale slugs are canonicalised on read)
	body, _ := json.Marshal([]string{"architecture", "architecture", "missing"})
	req = httptest.NewRequest("PUT", "/api/favourites", strings.NewReader(string(body)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/favourites", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	got = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0] != "architecture" {
		t.Fatalf("expected [architecture] (dupes+stale dropped), got %v", got)
	}

	// Malformed body → 400
	req = httptest.NewRequest("PUT", "/api/favourites", strings.NewReader(`{"not":"an array"}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed body, got %d", rec.Code)
	}
}

func TestAPIDeleteDoc(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("DELETE", "/api/docs/architecture", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Deleted file is gone
	req = httptest.NewRequest("GET", "/api/docs/architecture", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rec.Code)
	}

	// The index doc cannot be deleted
	req = httptest.NewRequest("DELETE", "/api/docs/index", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 deleting index, got %d", rec.Code)
	}

	// Unknown doc → 404
	req = httptest.NewRequest("DELETE", "/api/docs/nonexistent", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 deleting unknown doc, got %d", rec.Code)
	}
}

func TestAPIRoutes_TicketsList(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/api/tickets", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var tickets []Ticket
	if err := json.Unmarshal(rec.Body.Bytes(), &tickets); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if len(tickets) < 2 {
		t.Errorf("expected at least 2 tickets, got %d", len(tickets))
	}
}

func TestAPIRoutes_TicketDetail(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/api/tickets/001", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var ticket Ticket
	if err := json.Unmarshal(rec.Body.Bytes(), &ticket); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if ticket.ID != "001" || ticket.Title != "Test ticket" {
		t.Errorf("ticket mismatch: %+v", ticket)
	}
	if len(ticket.Comments) != 1 || ticket.Comments[0].Text != "First comment." {
		t.Errorf("ticket comments mismatch: %+v", ticket.Comments)
	}
}

func TestAPIRoutes_TicketDetailNotFound(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/api/tickets/999", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestAPIRoutes_ThreadsListAndCreate(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	// Create Thread
	payload := map[string]string{
		"context": "doc-index",
		"title":   "Conversation about home",
	}
	payloadBytes, _ := json.Marshal(payload)

	reqCreate := httptest.NewRequest("POST", "/api/threads", bytes.NewBuffer(payloadBytes))
	recCreate := httptest.NewRecorder()
	mux.ServeHTTP(recCreate, reqCreate)

	if recCreate.Code != http.StatusOK {
		t.Fatalf("expected status 200 on create, got %d: %s", recCreate.Code, recCreate.Body.String())
	}

	var newThread map[string]interface{}
	_ = json.Unmarshal(recCreate.Body.Bytes(), &newThread)
	threadID, _ := newThread["id"].(string)

	if threadID == "" {
		t.Fatal("expected non-empty thread id")
	}

	// List Threads
	reqList := httptest.NewRequest("GET", "/api/threads", nil)
	recList := httptest.NewRecorder()
	mux.ServeHTTP(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Errorf("expected status 200 on list, got %d", recList.Code)
	}

	var threads []map[string]interface{}
	_ = json.Unmarshal(recList.Body.Bytes(), &threads)
	if len(threads) < 1 {
		t.Errorf("expected at least 1 thread, got %d", len(threads))
	}

	// Get specific thread
	reqGet := httptest.NewRequest("GET", "/api/threads/"+threadID, nil)
	recGet := httptest.NewRecorder()
	mux.ServeHTTP(recGet, reqGet)

	if recGet.Code != http.StatusOK {
		t.Errorf("expected status 200 on get, got %d", recGet.Code)
	}

	var fetchedThread map[string]interface{}
	_ = json.Unmarshal(recGet.Body.Bytes(), &fetchedThread)
	if fetchedThread["id"] != threadID {
		t.Errorf("expected thread ID '%s', got '%s'", threadID, fetchedThread["id"])
	}

	// Delete specific thread
	reqDel := httptest.NewRequest("DELETE", "/api/threads/"+threadID, nil)
	recDel := httptest.NewRecorder()
	mux.ServeHTTP(recDel, reqDel)

	if recDel.Code != http.StatusNoContent {
		t.Errorf("expected status 204 on delete, got %d", recDel.Code)
	}

	// Verify thread is deleted
	reqGetAfterDel := httptest.NewRequest("GET", "/api/threads/"+threadID, nil)
	recGetAfterDel := httptest.NewRecorder()
	mux.ServeHTTP(recGetAfterDel, reqGetAfterDel)

	if recGetAfterDel.Code != http.StatusNotFound {
		t.Errorf("expected status 404 after delete, got %d", recGetAfterDel.Code)
	}
}

func TestAPIRoutes_Config(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/api/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	var config Config
	if err := json.Unmarshal(rec.Body.Bytes(), &config); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if config.BaseURL == "" || config.Model == "" {
		t.Errorf("expected non-empty config values: %+v", config)
	}
}

// initRevisionHTTPEnv builds the standard git layout (repo at tmp root,
// DEVTOP_DIR = <root>/.devtop) and returns a mux with the revision routes.
func initRevisionHTTPEnv(t *testing.T) *http.ServeMux {
	tmp, parent := setupGitHistory(t)
	t.Cleanup(func() { os.RemoveAll(tmp) })
	_ = parent

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/revisions/docs/{slug...}", handleAPIDocRevisions)
	mux.HandleFunc("GET /api/revisions/tickets/{id}", handleAPITicketRevisions)
	return mux
}

func TestAPIDocRevisionsList(t *testing.T) {
	mux := initRevisionHTTPEnv(t)

	req := httptest.NewRequest("GET", "/api/revisions/docs/architecture", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var revs []Revision
	if err := json.Unmarshal(rec.Body.Bytes(), &revs); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("expected 2 revisions, got %d", len(revs))
	}
	if !revs[0].IsCurrent {
		t.Errorf("expected newest revision marked current")
	}
	// Router must be matched, so 404s probe routing not handler logic.
	req = httptest.NewRequest("GET", "/api/revisions/docs/missing", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing doc, got %d", rec.Code)
	}
}

func TestAPIDocRevisionsAt(t *testing.T) {
	mux := initRevisionHTTPEnv(t)

	// Deleted-at-commit simulation: request content at the parent commit.
	req := httptest.NewRequest("GET", "/api/revisions/docs/architecture?at=HEAD~1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if body["title"] != "System Architecture" {
		t.Errorf("expected title, got %v", body["title"])
	}
	content, _ := body["content"].(string)
	if !strings.Contains(content, "Go + Alpine") {
		t.Errorf("expected old content, got %q", content)
	}
	if deleted, _ := body["deleted"].(bool); deleted {
		t.Errorf("expected deleted=false at parent commit")
	}

	// Bad commit -> 400.
	req = httptest.NewRequest("GET", "/api/revisions/docs/architecture?at=notarealsha", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid commit, got %d", rec.Code)
	}
}

func TestAPIDocRevisionsDiff(t *testing.T) {
	mux := initRevisionHTTPEnv(t)

	req := httptest.NewRequest("GET", "/api/revisions/docs/architecture?a=HEAD~1&b=HEAD", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if !strings.Contains(body["diff"], "-Stack: Go + Alpine.") ||
		!strings.Contains(body["diff"], "+Stack: Go + React.") {
		t.Errorf("expected unified diff body, got %q", body["diff"])
	}
}
