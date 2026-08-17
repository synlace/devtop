package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestPaths(t *testing.T) RepoPaths {
	t.Helper()
	root := t.TempDir()
	return RepoPaths{
		DevTop:  filepath.Join(root, ".devtop"),
		Docs:    filepath.Join(root, ".devtop", "docs"),
		Tickets: filepath.Join(root, ".devtop", "tickets"),
		Threads: filepath.Join(root, ".devtop", "threads"),
		Data:    filepath.Join(root, ".devtop", "data"),
	}
}

// TestScaffoldRepo_MaterializesDefaults verifies the single materialization
// path produces the complete .devtop: config, kind dirs, default agents,
// default skills, and the welcome doc.
func TestScaffoldRepo_MaterializesDefaults(t *testing.T) {
	p := newTestPaths(t)
	if err := scaffoldRepo(p); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"config.yml",
		"agents/docs.mdx",
		"agents/doc-builder.mdx",
		"agents/semantics-builder.mdx",
		"agents/ticket-deriver.mdx",
		"skills/ste100.mdx",
		"skills/acceptance-criteria.mdx",
		"skills/artifact-review.mdx",
		"intents/",
		"documentation/",
		"requirements/",
		"docs/index.mdx",
	} {
		if _, err := os.Stat(filepath.Join(p.DevTop, f)); err != nil {
			t.Fatalf("%s not scaffolded: %v", f, err)
		}
	}
	root := filepath.Dir(p.DevTop)
	if data, err := os.ReadFile(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatalf("root AGENTS.md not scaffolded: %v", err)
	} else if !strings.Contains(string(data), "implement the open tickets") {
		t.Error("root AGENTS.md lacks the implementer entry line")
	}
	data, err := os.ReadFile(filepath.Join(p.DevTop, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "default: docs") {
		t.Error("scaffolded config.yml does not set the docs agent as default")
	}
}

// TestScaffoldRepo_KeepsRepoAuthoredFiles verifies scaffold is
// non-destructive: files already present are never overwritten, while missing
// defaults are still added.
func TestScaffoldRepo_KeepsRepoAuthoredFiles(t *testing.T) {
	p := newTestPaths(t)
	agents := filepath.Join(p.DevTop, "agents")
	if err := os.MkdirAll(agents, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, "docs.mdx"), []byte("---\ntitle: Custom\n---\ncustom body"), 0644); err != nil {
		t.Fatal(err)
	}
	// A repo-authored root AGENTS.md must survive the scaffold untouched.
	root := filepath.Dir(p.DevTop)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("repo contract"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := scaffoldRepo(p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(agents, "docs.mdx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "custom body") {
		t.Error("repo-authored agent was overwritten by the scaffold")
	}
	if _, err := os.Stat(filepath.Join(agents, "semantics-builder.mdx")); err != nil {
		t.Error("missing default agent not scaffolded alongside repo files")
	}
	contract, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(contract)) != "repo contract" {
		t.Error("repo-authored root AGENTS.md was overwritten by the scaffold")
	}
}

// TestRepoInit_DirectScaffold verifies the UI "Initialize" action goes
// through the same complete scaffold.
func TestRepoInit_DirectScaffold(t *testing.T) {
	prev := DEVTOP_DIR
	defer func() { DEVTOP_DIR = prev }()

	root := t.TempDir()
	p := newTestPaths(t)
	DEVTOP_DIR = p.DevTop
	r := newRepo(root)
	r.paths = p

	if err := r.Init(); err != nil {
		t.Fatal(err)
	}
	if st := r.Status(); st.Status != "nogit" || !st.Initialized {
		t.Fatalf("expected nogit initialized, got %+v", st)
	}
	for _, agent := range []string{"docs", "doc-builder", "semantics-builder", "ticket-deriver"} {
		if _, err := os.Stat(filepath.Join(p.DevTop, "agents", agent+".mdx")); err != nil {
			t.Errorf("agent %s not scaffolded by Repo.Init: %v", agent, err)
		}
	}
}
