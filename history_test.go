package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupGitHistory builds the standard layout — a git repo at the workspace
// root with DEVTOP_DIR = <root>/.devtop — and commits a doc twice so there is
// real history to inspect. Returns the tmp root and the parent commit SHA.
func setupGitHistory(t *testing.T) (string, string) {
	t.Helper()
	tmpRoot, err := os.MkdirTemp("", "devtop-hist-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}

	DEVTOP_DIR = filepath.Join(tmpRoot, ".devtop")
	DOCS_DIR = filepath.Join(DEVTOP_DIR, "docs")
	TICKETS_DIR = filepath.Join(DEVTOP_DIR, "tickets")
	THREADS_DIR = filepath.Join(DEVTOP_DIR, "threads")
	DATA_DIR = filepath.Join(DEVTOP_DIR, "data")
	_ = os.MkdirAll(DOCS_DIR, 0755)
	_ = os.MkdirAll(TICKETS_DIR, 0755)
	_ = os.MkdirAll(THREADS_DIR, 0755)

	doc := filepath.Join(DOCS_DIR, "architecture.mdx")
	writeDoc := func(body string) {
		_ = os.WriteFile(doc, []byte("---\ntitle: \"System Architecture\"\n---\n\n"+body), 0644)
	}
	// V1 — the committed state at the parent commit.
	writeDoc("# Architecture\nStack: Go + Alpine.")

	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("add", ".")
	run("commit", "-q", "-m", "Initial commit")

	// V2 — changed and committed so there is a real diff.
	parent := run("rev-parse", "HEAD")
	writeDoc("# Architecture\nStack: Go + React.")
	run("add", ".")
	run("commit", "-q", "-m", "Update docs stack")

	return tmpRoot, parent
}

func TestListRevisions(t *testing.T) {
	tmp, parent := setupGitHistory(t)
	defer os.RemoveAll(tmp)

	path := filepath.Join(DOCS_DIR, "architecture.mdx")
	revs, err := listRevisions(path)
	if err != nil {
		t.Fatalf("listRevisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("expected 2 revisions, got %d", len(revs))
	}
	if !revs[0].IsCurrent {
		t.Errorf("expected newest revision marked current")
	}
	if revs[1].SHA != parent {
		t.Errorf("expected parent commit %s second, got %s", parent, revs[1].SHA)
	}
	if revs[1].Message != "Initial commit" {
		t.Errorf("expected 'Initial commit', got %q", revs[1].Message)
	}
}

func TestContentAt(t *testing.T) {
	tmp, parent := setupGitHistory(t)
	defer os.RemoveAll(tmp)

	path := filepath.Join(DOCS_DIR, "architecture.mdx")

	_, currentContent, _, err := contentAt(path, "HEAD")
	if err != nil {
		t.Fatalf("contentAt HEAD: %v", err)
	}
	if !strings.Contains(currentContent, "Go + React") {
		t.Errorf("expected 'Go + React' at HEAD, got %q", currentContent)
	}

	title, parentContent, _, err := contentAt(path, parent)
	if err != nil {
		t.Fatalf("contentAt parent: %v", err)
	}
	if title != "System Architecture" {
		t.Errorf("expected title from frontmatter, got %q", title)
	}
	if !strings.Contains(parentContent, "Go + Alpine") {
		t.Errorf("expected 'Go + Alpine' at parent, got %q", parentContent)
	}
}

func TestDiffBetween(t *testing.T) {
	tmp, parent := setupGitHistory(t)
	defer os.RemoveAll(tmp)

	path := filepath.Join(DOCS_DIR, "architecture.mdx")
	diff, err := diffBetween(path, parent, "HEAD")
	if err != nil {
		t.Fatalf("diffBetween: %v", err)
	}
	if !strings.Contains(diff, "-Stack: Go + Alpine.") || !strings.Contains(diff, "+Stack: Go + React.") {
		t.Errorf("expected +/- lines in unified diff:\n%s", diff)
	}
}

func TestContentAtDeleted(t *testing.T) {
	tmpRoot, err := os.MkdirTemp("", "devtop-hist-del-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(tmpRoot)

	DEVTOP_DIR = filepath.Join(tmpRoot, ".devtop")
	DOCS_DIR = filepath.Join(DEVTOP_DIR, "docs")
	_ = os.MkdirAll(DOCS_DIR, 0755)

	doc := filepath.Join(DOCS_DIR, "gone.mdx")
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	_ = os.WriteFile(doc, []byte("---\ntitle: \"Gone Doc\"\n---\n\n# Gone\nExisted once."), 0644)
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("add", ".")
	run("commit", "-q", "-m", "Add gone doc")

	living := run("rev-parse", "HEAD")
	_ = os.Remove(doc)
	run("add", "-A")
	run("commit", "-q", "-m", "Remove gone doc")
	gone := run("rev-parse", "HEAD")

	// At the commit before removal the doc is readable and not deleted.
	title, content, deleted, err := contentAt(doc, living)
	if err != nil {
		t.Fatalf("contentAt living: %v", err)
	}
	if deleted {
		t.Errorf("expected deleted=false at living commit")
	}
	if title != "Gone Doc" || !strings.Contains(content, "Existed once.") {
		t.Errorf("expected content at living commit, got title=%q content=%q", title, content)
	}

	// At the removal commit it is flagged deleted, not an error.
	if _, _, deleted, err := contentAt(doc, gone); err != nil || deleted != true {
		t.Errorf("expected deleted=true at removing commit, got deleted=%v err=%v", deleted, err)
	}
}

func TestFindRepoRoot(t *testing.T) {
	// Repo at workspace root, .devtop below it — the standard layout.
	tmp, _ := setupGitHistory(t)
	defer os.RemoveAll(tmp)

	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	if root != tmp {
		t.Errorf("expected repo root %s, got %s", tmp, root)
	}

	// A bare temp dir with no git anywhere around it must error.
	empty, err := os.MkdirTemp("", "devtop-nogit-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(empty)
	DEVTOP_DIR = filepath.Join(empty, ".devtop")
	_ = os.MkdirAll(DEVTOP_DIR, 0755)
	if _, err := findRepoRoot(); err == nil {
		t.Errorf("expected error for non-git directory, got nil")
	}
}