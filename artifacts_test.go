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

// writeFixturePRD seeds a prds dir with a sample PRD so the generic artifact
// endpoints have something to serve.
func writeFixturePRD(t *testing.T, id, frontmatter, body string) {
	t.Helper()
	prdsDir := filepath.Join(DEVTOP_DIR, "prds")
	if err := os.MkdirAll(prdsDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" + frontmatter + "---\n\n" + body
	if err := os.WriteFile(filepath.Join(prdsDir, id+".mdx"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestAPIArtifacts_List(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	writeFixturePRD(t, "onboarding", "title: \"Onboarding Flow\"\nstatus: draft\n", "# Onboarding\n\nBody.")
	writeFixturePRD(t, "billing", "title: \"Billing v2\"\nstatus: approved\n", "# Billing\n\nBody.")

	req := httptest.NewRequest("GET", "/api/artifacts/prds", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var items []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 prds, got %d", len(items))
	}
	if items[0]["id"] != "billing" || items[0]["title"] != "Billing v2" || items[0]["status"] != "approved" {
		t.Errorf("unexpected first item: %+v", items[0])
	}
	if items[1]["id"] != "onboarding" {
		t.Errorf("unexpected second item: %+v", items[1])
	}
}

func TestAPIArtifacts_Detail(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	writeFixturePRD(t, "onboarding", "title: \"Onboarding Flow\"\nstatus: draft\n", "# Onboarding\n\nAcceptance body.")

	req := httptest.NewRequest("GET", "/api/artifacts/prds/onboarding", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out struct {
		ID          string                 `json:"id"`
		Title       string                 `json:"title"`
		Content     string                 `json:"content"`
		Frontmatter map[string]interface{} `json:"frontmatter"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "onboarding" || out.Title != "Onboarding Flow" {
		t.Errorf("unexpected item: %+v", out)
	}
	if !strings.Contains(out.Content, "<h1>Onboarding</h1>") {
		t.Errorf("content not rendered as HTML: %q", out.Content)
	}
	if out.Frontmatter["status"] != "draft" {
		t.Errorf("frontmatter missing status: %+v", out.Frontmatter)
	}
}

func TestAPIArtifacts_UnknownKind(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/api/artifacts/nope", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown kind, got %d", rec.Code)
	}
}

func TestAPIArtifacts_DetailNotFound(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/api/artifacts/prds/missing", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing item, got %d", rec.Code)
	}
}

func TestAPIArtifacts_ListMissingDir(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	// Simulate a fresh repo where the kind's directory hasn't been created
	// (main.go's ensureKindDirs normally creates it at startup). An empty
	// kind is a valid state: the engine must return [] instead of 500.
	_ = os.RemoveAll(filepath.Join(DEVTOP_DIR, "prds"))

	req := httptest.NewRequest("GET", "/api/artifacts/prds", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for missing kind dir, got %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("expected empty array, got %q", got)
	}
}

func TestAPIArtifacts_ListEmptyDir(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	// An existing but empty kind dir must still yield [], not null.
	if err := os.MkdirAll(filepath.Join(DEVTOP_DIR, "prds"), 0755); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/artifacts/prds", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("expected empty array, got %q", got)
	}
}

func TestAPIArtifacts_DetailTraversalGuard(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	// Encoded traversal: ServeMux redirects literal ".." before the handler,
	// so exercise the handler's own guard with %2e%2e.
	req := httptest.NewRequest("GET", "/api/artifacts/prds/%2e%2e/config.yml", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for traversal, got %d", rec.Code)
	}
}
