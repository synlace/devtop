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

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func TestGuardPath(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "docs")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	if p, err := guardPath(root, "a/b.md"); err != nil {
		t.Fatalf("plain path: %v", err)
	} else if p != filepath.Join(root, "a", "b.md") {
		t.Fatalf("got %q", p)
	}

	for _, bad := range []string{"../x.md", "../../x.md", "a/../../x.md", "..", "/abs.md"} {
		if _, err := guardPath(root, bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}

	// Symlinked parent of a not-yet-created file must be caught.
	outside := t.TempDir()
	link := filepath.Join(sub, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := guardPath(sub, "out/x.md"); err == nil {
		t.Fatal("expected symlinked-parent escape to be rejected")
	}
	// An existing file reached through the symlink is rejected too.
	if err := os.WriteFile(filepath.Join(outside, "x.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := guardPath(sub, "out/x.md"); err == nil {
		t.Fatal("expected symlinked-file escape to be rejected")
	}
	// A symlink that stays inside is fine.
	inside := filepath.Join(sub, "real.md")
	if err := os.WriteFile(inside, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(sub, "alias.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := guardPath(sub, "alias.md"); err != nil {
		t.Fatalf("inner symlink rejected: %v", err)
	}
}

func TestHandleInternalTool(t *testing.T) {
	t.Setenv("DEVTOP_TOOL_SANDBOX", "0")
	cleanRegistry(t)
	root := newRepoTemp(t, "itool", true)
	if _, err := registry.Add(root); err != nil {
		t.Fatal(err)
	}
	repo := registry.List()[0]

	do := func(name string, args map[string]interface{}, header string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]interface{}{"name": name, "args": args})
		req := httptest.NewRequest("POST", "/api/internal/tool", bytes.NewBuffer(body))
		if header != "" {
			req.Header.Set("X-Devtop-Repo", header)
		}
		rr := httptest.NewRecorder()
		handleAPIInternalTool(rr, req)
		return rr
	}

	rr := do("write_doc", map[string]interface{}{"path": "a.md", "content": "x"}, repo.Name)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !contains(resp.Result, "Written to docs/a.md") {
		t.Fatalf("unexpected result: %q", resp.Result)
	}
	if _, err := os.Stat(filepath.Join(repo.paths.Docs, "a.md")); err != nil {
		t.Fatalf("doc not written: %v", err)
	}

	// Traversal is rejected through the endpoint.
	rr = do("write_doc", map[string]interface{}{"path": "../../evil.md", "content": "x"}, repo.Name)
	if contains(rr.Body.String(), "Written") {
		t.Fatal("traversal write succeeded through internal tool")
	}

	// Unknown repo -> 404.
	rr = do("list_docs", map[string]interface{}{}, "missing-repo")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown repo status %d", rr.Code)
	}

	// Tool dispatch scoped to the repo root, not the workspace.
	if err := os.WriteFile(filepath.Join(repo.Root, "probe.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	rr = do("read_workspace_file", map[string]interface{}{"path": "probe.md"}, repo.Name)
	if contains(rr.Body.String(), "not found") || contains(rr.Body.String(), "Error") {
		t.Fatalf("workspace read not repo-scoped: %s", rr.Body.String())
	}
}

func TestHandleAPIRepoDetail(t *testing.T) {
	cleanRegistry(t)
	root := newRepoTemp(t, "rdetail", true)
	if _, err := registry.Add(root); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/repos/rdetail", nil)
	req.SetPathValue("name", "rdetail")
	rr := httptest.NewRecorder()
	handleAPIRepoDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var d map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d["root"] != root || d["docs"] != filepath.Join(root, ".devtop", "docs") {
		t.Fatalf("bad detail: %v", d)
	}

	req = httptest.NewRequest("GET", "/api/repos/nope", nil)
	req.SetPathValue("name", "nope")
	rr = httptest.NewRecorder()
	handleAPIRepoDetail(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing repo status %d", rr.Code)
	}
}
