package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestEnvironment(t *testing.T) string {
	tempDir := setupTestDirs(t)

	DEVTOP_DIR = filepath.Join(tempDir, ".devtop")

	_ = os.MkdirAll(DOCS_DIR, 0755)
	_ = os.MkdirAll(TICKETS_DIR, 0755)
	_ = os.MkdirAll(THREADS_DIR, 0755)
	_ = os.MkdirAll(DATA_DIR, 0755)

	_ = os.WriteFile(filepath.Join(DOCS_DIR, "index.mdx"), []byte("---\ntitle: \"Project Overview\"\n---\n\n# Welcome\n\nTest documentation."), 0644)
	_ = os.WriteFile(filepath.Join(DOCS_DIR, "architecture.mdx"), []byte("---\ntitle: \"System Architecture\"\n---\n\n# Architecture\n\nStack: Go."), 0644)

	_ = os.WriteFile(filepath.Join(TICKETS_DIR, "001.md"), []byte("---\nid: \"001\"\ntitle: \"Test ticket\"\nstatus: \"open\"\npriority: \"high\"\nassignee: \"alice\"\ncreated: \"2026-07-27\"\n---\n\nDescription text.\n\n**2026-07-27 10:00** — alice: First comment.\n"), 0644)
	_ = os.WriteFile(filepath.Join(TICKETS_DIR, "002.md"), []byte("---\nid: \"002\"\ntitle: \"Second ticket\"\nstatus: \"done\"\npriority: \"low\"\nassignee: \"\"\ncreated: \"2026-07-26\"\n---\n\nAnother ticket.\n"), 0644)

	return tempDir
}

func TestDocTools_ReadDoc(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	res := dispatchTool("read_doc", map[string]interface{}{"path": "nonexistent.mdx"})
	if !strings.Contains(res, "Error") {
		t.Errorf("expected error, got: %s", res)
	}

	res = dispatchTool("read_doc", map[string]interface{}{"path": "index.mdx"})
	if !strings.Contains(res, "Welcome") {
		t.Errorf("expected content, got: %s", res)
	}

	_ = os.MkdirAll(filepath.Join(DOCS_DIR, "sub"), 0755)
	_ = os.WriteFile(filepath.Join(DOCS_DIR, "sub", "deep.mdx"), []byte("# Deep doc"), 0644)
	res = dispatchTool("read_doc", map[string]interface{}{"path": "sub/deep.mdx"})
	if !strings.Contains(res, "Deep doc") {
		t.Errorf("expected content from subdir, got: %s", res)
	}
}

func TestDocTools_WriteDoc(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	content := "---\ntitle: New\n---\n\n# New doc"
	res := dispatchTool("write_doc", map[string]interface{}{"path": "new.mdx", "content": content})
	if !strings.Contains(res, "Written to docs/new.mdx") {
		t.Errorf("expected success message, got: %s", res)
	}

	fileBytes, _ := os.ReadFile(filepath.Join(DOCS_DIR, "new", "index.mdx"))
	if string(fileBytes) != content {
		t.Errorf("file content mismatch, got: %s", string(fileBytes))
	}

	content2 := "# Overwritten"
	dispatchTool("write_doc", map[string]interface{}{"path": "index.mdx", "content": content2})
	fileBytes2, _ := os.ReadFile(filepath.Join(DOCS_DIR, "index.mdx"))
	if string(fileBytes2) != content2 {
		t.Errorf("file content mismatch, got: %s", string(fileBytes2))
	}

	content3 := "# Nested"
	dispatchTool("write_doc", map[string]interface{}{"path": "a/b/c/doc.mdx", "content": content3})
	fileBytes3, _ := os.ReadFile(filepath.Join(DOCS_DIR, "a/b/c/doc.mdx"))
	if string(fileBytes3) != content3 {
		t.Errorf("nested file content mismatch, got: %s", string(fileBytes3))
	}
}

func TestDocTools_ListDocs(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	res := dispatchTool("list_docs", map[string]interface{}{})
	var docs []DocSlug
	if err := json.Unmarshal([]byte(res), &docs); err != nil {
		t.Fatalf("failed to unmarshal docs list: %v", err)
	}

	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}

	if docs[0].Slug != "architecture" || docs[0].Title != "System Architecture" {
		t.Errorf("unexpected doc at index 0: %+v", docs[0])
	}
	if docs[1].Slug != "index" || docs[1].Title != "Project Overview" {
		t.Errorf("unexpected doc at index 1: %+v", docs[1])
	}
}

func TestTicketTools_ListTickets(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	res := dispatchTool("list_tickets", map[string]interface{}{})
	var tickets []Ticket
	if err := json.Unmarshal([]byte(res), &tickets); err != nil {
		t.Fatalf("failed to unmarshal ticket list: %v", err)
	}

	if len(tickets) < 2 {
		t.Fatalf("expected at least 2 tickets, got %d", len(tickets))
	}

	found001 := false
	found002 := false
	for _, tk := range tickets {
		if tk.ID == "001" {
			found001 = true
			if tk.Title != "Test ticket" || tk.Status != "open" || tk.Priority != "high" || tk.Assignee != "alice" {
				t.Errorf("ticket 001 metadata mismatch: %+v", tk)
			}
		}
		if tk.ID == "002" {
			found002 = true
			if tk.Title != "Second ticket" || tk.Status != "done" || tk.Priority != "low" || tk.Assignee != "" {
				t.Errorf("ticket 002 metadata mismatch: %+v", tk)
			}
		}
	}
	if !found001 || !found002 {
		t.Error("could not find both 001 and 002 tickets")
	}

	_ = os.RemoveAll(TICKETS_DIR)
	_ = os.MkdirAll(TICKETS_DIR, 0755)
	resEmpty := dispatchTool("list_tickets", map[string]interface{}{})
	var ticketsEmpty []Ticket
	_ = json.Unmarshal([]byte(resEmpty), &ticketsEmpty)
	if len(ticketsEmpty) != 0 {
		t.Errorf("expected empty list, got %d items", len(ticketsEmpty))
	}
}

func TestTicketTools_ReadTicket(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	res := dispatchTool("read_ticket", map[string]interface{}{"id": "999"})
	if !strings.Contains(res, "Error") {
		t.Errorf("expected error message, got: %s", res)
	}

	res = dispatchTool("read_ticket", map[string]interface{}{"id": "001"})
	if !strings.Contains(res, "Description text.") {
		t.Errorf("expected ticket content, got: %s", res)
	}
}

func TestTicketTools_GetNextTicketID(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	nextID := getNextTicketID()
	if nextID != "003" {
		t.Errorf("expected next ticket ID 003, got %s", nextID)
	}

	_ = os.RemoveAll(TICKETS_DIR)
	_ = os.MkdirAll(TICKETS_DIR, 0755)
	nextIDEmpty := getNextTicketID()
	if nextIDEmpty != "001" {
		t.Errorf("expected first ticket ID 001, got %s", nextIDEmpty)
	}
}

func TestTicketTools_CreateTicket(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	res := dispatchTool("create_ticket", map[string]interface{}{
		"title":       "Bug found",
		"description": "Description of bug",
		"priority":    "urgent",
		"assignee":    "bob",
	})
	if !strings.Contains(res, "Created ticket 003") {
		t.Errorf("expected success message, got: %s", res)
	}

	filePath := filepath.Join(TICKETS_DIR, "003.md")
	contentBytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read created ticket file: %v", err)
	}
	content := string(contentBytes)
	if !strings.Contains(content, `title: "Bug found"`) ||
		!strings.Contains(content, `priority: "urgent"`) ||
		!strings.Contains(content, `assignee: "bob"`) ||
		!strings.Contains(content, `status: "open"`) ||
		!strings.Contains(content, "Description of bug") {
		t.Errorf("created ticket file lacks expected content: %s", content)
	}

	tkt, err := getTicket("003")
	if err != nil {
		t.Fatalf("ticket 003 not found: %v", err)
	}
	if tkt.Title != "Bug found" || tkt.Priority != "urgent" || tkt.Assignee != "bob" {
		t.Errorf("ticket mismatch: %+v", tkt)
	}
}

func TestTicketTools_UpdateTicket(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	res := dispatchTool("update_ticket", map[string]interface{}{
		"id":     "001",
		"status": "in-progress",
	})
	if !strings.Contains(res, "Updated ticket 001") {
		t.Errorf("expected success message, got: %s", res)
	}

	tkt, _ := getTicket("001")
	if tkt.Status != "in-progress" {
		t.Errorf("expected status 'in-progress', got '%s'", tkt.Status)
	}

	dispatchTool("update_ticket", map[string]interface{}{
		"id":       "001",
		"assignee": "bob",
	})
	tkt, _ = getTicket("001")
	if tkt.Assignee != "bob" {
		t.Errorf("expected assignee 'bob', got '%s'", tkt.Assignee)
	}

	resError := dispatchTool("update_ticket", map[string]interface{}{
		"id":     "999",
		"status": "done",
	})
	if !strings.Contains(resError, "Error") {
		t.Errorf("expected error, got: %s", resError)
	}
}

func TestTicketTools_AddComment(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	res := dispatchTool("add_comment", map[string]interface{}{
		"id":   "001",
		"body": "New comment here",
	})
	if !strings.Contains(res, "Comment added") {
		t.Errorf("expected success message, got: %s", res)
	}

	contentBytes, _ := os.ReadFile(filepath.Join(TICKETS_DIR, "001.md"))
	content := string(contentBytes)
	if !strings.Contains(content, "New comment here") {
		t.Errorf("comment not found in ticket file: %s", content)
	}

	tkt, _ := getTicket("001")
	if !strings.Contains(tkt.RawDescription, "New comment here") {
		t.Errorf("comment not found in ticket description: %s", tkt.RawDescription)
	}

	resError := dispatchTool("add_comment", map[string]interface{}{
		"id":   "999",
		"body": "Comment",
	})
	if !strings.Contains(resError, "Error") {
		t.Errorf("expected error, got: %s", resError)
	}
}

func TestGitCommit(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	gitRepoRoot := filepath.Dir(DEVTOP_DIR)

	initCmd := exec.Command("git", "init")
	initCmd.Dir = gitRepoRoot
	_ = initCmd.Run()

	configUser := exec.Command("git", "config", "user.name", "Test User")
	configUser.Dir = gitRepoRoot
	_ = configUser.Run()

	configEmail := exec.Command("git", "config", "user.email", "test@example.com")
	configEmail.Dir = gitRepoRoot
	_ = configEmail.Run()

	dummyFile := filepath.Join(DEVTOP_DIR, "dummy.txt")
	_ = os.WriteFile(dummyFile, []byte("hello"), 0644)

	res := dispatchTool("git_commit", map[string]interface{}{"message": "test commit message"})
	if !strings.Contains(res, "Committed") && !strings.Contains(res, "Nothing to commit") {
		t.Errorf("git_commit output did not match expected: %s", res)
	}
}

func TestWorkspaceTools_ReadFile(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	root := workspaceRoot()
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	_ = os.MkdirAll(filepath.Join(root, "src"), 0755)
	_ = os.WriteFile(filepath.Join(root, "src", "util.go"), []byte("package src\n"), 0644)

	res := dispatchTool("read_workspace_file", map[string]interface{}{"path": "main.go"})
	if !strings.Contains(res, "package main") {
		t.Errorf("expected content, got: %s", res)
	}

	res = dispatchTool("read_workspace_file", map[string]interface{}{"path": "src/util.go"})
	if !strings.Contains(res, "package src") {
		t.Errorf("expected nested content, got: %s", res)
	}

	res = dispatchTool("read_workspace_file", map[string]interface{}{"path": "missing.txt"})
	if !strings.Contains(res, "Error") {
		t.Errorf("expected error for missing file, got: %s", res)
	}

	res = dispatchTool("read_workspace_file", map[string]interface{}{"path": "."})
	if !strings.Contains(res, "directory") {
		t.Errorf("expected directory error, got: %s", res)
	}
}

func TestWorkspaceTools_TraversalBlocked(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	outside := filepath.Join(filepath.Dir(tempDir), "outside-secret.txt")
	_ = os.WriteFile(outside, []byte("secret"), 0644)
	defer os.Remove(outside)

	for _, p := range []string{"../../etc/passwd", "../outside-secret.txt", "/etc/passwd", "/etc/hostname"} {
		res := dispatchTool("read_workspace_file", map[string]interface{}{"path": p})
		if !strings.Contains(res, "Error") {
			t.Errorf("path %q should be blocked, got: %s", p, res)
		}
	}

	link := filepath.Join(tempDir, "escape")
	_ = os.Symlink("/etc/hostname", link)
	res := dispatchTool("read_workspace_file", map[string]interface{}{"path": "escape"})
	if !strings.Contains(res, "Error") {
		t.Errorf("symlink escape should be blocked, got: %s", res)
	}

	res = dispatchTool("list_workspace_files", map[string]interface{}{"path": "../outside"})
	if !strings.Contains(res, "Error") {
		t.Errorf("list traversal should be blocked, got: %s", res)
	}
}

func TestWorkspaceTools_BinaryAndSizeLimits(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	root := workspaceRoot()
	binary := filepath.Join(root, "app.bin")
	_ = os.WriteFile(binary, []byte{0x00, 0x01, 0x02}, 0644)
	res := dispatchTool("read_workspace_file", map[string]interface{}{"path": "app.bin"})
	if !strings.Contains(res, "Binary file") {
		t.Errorf("expected binary notice, got: %s", res)
	}

	big := filepath.Join(root, "big.log")
	_ = os.WriteFile(big, make([]byte, MAX_WORKSPACE_READ_BYTES+1), 0644)
	res = dispatchTool("read_workspace_file", map[string]interface{}{"path": "big.log"})
	if !strings.Contains(res, "too large") {
		t.Errorf("expected size limit error, got: %s", res)
	}
}

func TestWorkspaceTools_ListFiles(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	root := workspaceRoot()
	_ = os.WriteFile(filepath.Join(root, "README.md"), []byte("# repo"), 0644)
	_ = os.MkdirAll(filepath.Join(root, "src"), 0755)
	_ = os.WriteFile(filepath.Join(root, "src", "util.go"), []byte("package src\n"), 0644)

	res := dispatchTool("list_workspace_files", map[string]interface{}{})
	if !strings.Contains(res, "README.md") || !strings.Contains(res, "src") {
		t.Errorf("expected README.md and src, got: %s", res)
	}
	if strings.Contains(res, ".devtop") {
		t.Errorf("expected .devtop to be skipped, got: %s", res)
	}

	res = dispatchTool("list_workspace_files", map[string]interface{}{"path": "src"})
	if !strings.Contains(res, "util.go") {
		t.Errorf("expected util.go, got: %s", res)
	}
}

func TestWelcomeDoc_MaterializesOnEmpty(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	_ = os.RemoveAll(DOCS_DIR)
	_ = os.MkdirAll(DOCS_DIR, 0755)

	if err := ensureWelcomeDoc(); err != nil {
		t.Fatalf("ensureWelcomeDoc failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(DOCS_DIR, "index.mdx"))
	if err != nil {
		t.Fatalf("welcome doc not written: %v", err)
	}
	if !strings.Contains(string(content), "Welcome to devtop") {
		t.Errorf("welcome doc missing expected text: %s", string(content))
	}

	docs, err := listDocs()
	if err != nil {
		t.Fatalf("listDocs failed: %v", err)
	}
	if len(docs) != 1 || docs[0].Slug != "index" {
		t.Fatalf("expected single index doc, got %+v", docs)
	}

	title, body, err := getDoc("index")
	if err != nil {
		t.Fatalf("getDoc(index) failed: %v", err)
	}
	if title != "Welcome to devtop" {
		t.Errorf("expected welcome title, got %q", title)
	}
	if !strings.Contains(body, "Welcome to devtop") {
		t.Errorf("expected rendered body, got: %s", body)
	}
}

func TestWelcomeDoc_NonDestructive(t *testing.T) {
	tempDir := initTestEnvironment(t)
	defer os.RemoveAll(tempDir)

	// initTestEnvironment already writes index.mdx + architecture.mdx.
	if err := ensureWelcomeDoc(); err != nil {
		t.Fatalf("ensureWelcomeDoc failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(DOCS_DIR, "index.mdx"))
	if err != nil {
		t.Fatalf("existing index.mdx must be preserved: %v", err)
	}
	if strings.Contains(string(content), "Welcome to devtop") {
		t.Errorf("welcome doc must not overwrite existing docs: %s", string(content))
	}
}

func TestWelcomeDoc_EmbeddedFallback(t *testing.T) {
	t.Parallel()

	title, body, err := getWelcomeDoc()
	if err != nil {
		t.Fatalf("getWelcomeDoc failed: %v", err)
	}
	if title != "Welcome to devtop" {
		t.Errorf("expected welcome title, got %q", title)
	}
	if !strings.Contains(body, "Welcome to devtop") {
		t.Errorf("expected rendered welcome body, got: %s", body)
	}
}
