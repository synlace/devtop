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
		"agents/classify-doc.mdx",
		"agents/prd-builder.mdx",
		"agents/ticket-deriver.mdx",
		"skills/ste100.mdx",
		"skills/acceptance-criteria.mdx",
		"skills/prd-review.mdx",
		"docs/index.mdx",
	} {
		if _, err := os.Stat(filepath.Join(p.DevTop, f)); err != nil {
			t.Fatalf("%s not scaffolded: %v", f, err)
		}
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
	if _, err := os.Stat(filepath.Join(agents, "prd-builder.mdx")); err != nil {
		t.Error("missing default agent not scaffolded alongside repo files")
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
	for _, agent := range []string{"docs", "classify-doc", "prd-builder", "ticket-deriver"} {
		if _, err := os.Stat(filepath.Join(p.DevTop, "agents", agent+".mdx")); err != nil {
			t.Errorf("agent %s not scaffolded by Repo.Init: %v", agent, err)
		}
	}
}