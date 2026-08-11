package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupAgentEnv points DEVTOP_DIR and engineConfig at a temp workspace with
// repo-owned agents/skills kinds, restoring the globals afterwards.
func setupAgentEnv(t *testing.T, defaultAgent string) string {
	prevDevtop := DEVTOP_DIR
	prevCfg := engineConfig
	tempDir := setupTestDirs(t)
	DEVTOP_DIR = filepath.Join(tempDir, ".devtop")
	_ = os.MkdirAll(DEVTOP_DIR, 0755)
	engineConfig = EngineConfig{
		ArtifactKinds: map[string]ArtifactKind{
			"agents": {Path: "agents", Extension: ".mdx"},
			"skills": {Path: "skills", Extension: ".mdx"},
		},
		AgentRuntime: AgentRuntimeConfig{Default: defaultAgent},
	}
	t.Cleanup(func() {
		DEVTOP_DIR = prevDevtop
		engineConfig = prevCfg
		os.RemoveAll(tempDir)
	})
	return filepath.Join(DEVTOP_DIR, "agents")
}

func writeAgentDef(t *testing.T, slug, content string) {
	t.Helper()
	agents := agentsDir()
	if err := os.MkdirAll(agents, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agents, slug+".mdx"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeSkillDef(t *testing.T, slug, content string) {
	t.Helper()
	dir := skillsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, slug+".mdx"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"docs/**", "docs/a.mdx", true},
		{"docs/**", "docs/a/b.mdx", true},
		{"docs/**", "docs/index.mdx", true},
		{"docs/**", "tickets/1.md", false},
		{"docs/*", "docs/a.mdx", true},
		{"docs/*", "docs/a/b.mdx", false},
		{"tickets/**", "tickets/001.md", true},
		{"prds/**", "docs/a.mdx", false},
		{"docs/**/*.mdx", "docs/a/b.mdx", true},
		{"docs/**", "docs", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.name); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestListAgentDefs_ParsesFrontmatter(t *testing.T) {
	setupAgentEnv(t, "")
	writeAgentDef(t, "author", `---
title: "Author"
tools: [base, docs]
skills: [ste100]
permissions:
  read: ["docs/**"]
  write: ["docs/**"]
---
You are the author agent.`)

	entries, err := listAgentDefs()
	if err != nil {
		t.Fatalf("listAgentDefs: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(entries))
	}
	if entries[0].Slug != "author" {
		t.Errorf("slug = %q", entries[0].Slug)
	}
	def := entries[0].Def
	if def.Title != "Author" {
		t.Errorf("title = %q", def.Title)
	}
	if len(def.Tools) != 2 || len(def.Skills) != 1 {
		t.Errorf("tools/skills not parsed: %+v", def)
	}
	if len(def.Permissions.Write) != 1 {
		t.Errorf("permissions not parsed: %+v", def.Permissions)
	}
	if !strings.Contains(def.Body, "You are the author agent") {
		t.Errorf("body not parsed: %q", def.Body)
	}
}

func TestBuildAgentRuntime_NotFound(t *testing.T) {
	setupAgentEnv(t, "")
	if _, err := buildAgentRuntime("missing"); err == nil {
		t.Fatal("expected error for unknown agent")
	}
}

func TestActiveRuntime_EmptyDefaultFallsBack(t *testing.T) {
	setupAgentEnv(t, "")
	writeAgentDef(t, "author", "---\ntitle: Author\n---\nAuthor body.\n")
	if rt := activeRuntime(); rt != nil {
		t.Fatalf("expected nil runtime for empty default, got %q", rt.Slug)
	}
}

func TestBuildAgentRuntime_AllowlistExpandsGroups(t *testing.T) {
	setupAgentEnv(t, "")
	writeAgentDef(t, "author", `---
title: "Author"
tools: [base, docs]
permissions:
  write: ["docs/**"]
---
Body.`)

	rt, err := buildAgentRuntime("author")
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	if rt.allowlist == nil {
		t.Fatal("allowlist not built")
	}
	for _, tool := range []string{"git_commit", "ask_user", "write_doc", "read_doc"} {
		if !rt.allowlist[tool] {
			t.Errorf("tool %q missing from allowlist", tool)
		}
	}
	for _, tool := range []string{"create_ticket", "add_comment"} {
		if rt.allowlist[tool] {
			t.Errorf("tool %q should be outside allowlist", tool)
		}
	}
}

func TestAuthorizeTool_AllowlistDenies(t *testing.T) {
	setupAgentEnv(t, "")
	writeAgentDef(t, "author", `---
title: "Author"
tools: [base]
permissions:
  write: ["tickets/**"]
---
Body.`)

	rt, err := buildAgentRuntime("author")
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	msg := rt.authorizeTool("list_docs", map[string]interface{}{})
	if msg != "" {
		t.Errorf("list_docs should be allowed, got %q", msg)
	}
	msg = rt.authorizeTool("create_ticket", map[string]interface{}{"title": "x", "description": "y", "priority": "low"})
	if !strings.Contains(msg, "not allowed") {
		t.Errorf("create_ticket should be denied by allowlist, got %q", msg)
	}
}

func TestAuthorizeTool_WriteScopes(t *testing.T) {
	setupAgentEnv(t, "")
	writeAgentDef(t, "author", `---
title: "Author"
tools: [docs, tickets]
permissions:
  write: ["docs/**"]
---
Body.`)

	rt, err := buildAgentRuntime("author")
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}

	if msg := rt.authorizeTool("write_doc", map[string]interface{}{"path": "new.mdx"}); msg != "" {
		t.Errorf("write_doc within scope should be allowed, got %q", msg)
	}
	if msg := rt.authorizeTool("write_doc", map[string]interface{}{"path": "sub/deep.mdx"}); msg != "" {
		t.Errorf("write_doc nested within scope should be allowed, got %q", msg)
	}
	if msg := rt.authorizeTool("create_ticket", map[string]interface{}{"id": "001"}); !strings.Contains(msg, "write scope") {
		t.Errorf("create_ticket outside scope should be denied, got %q", msg)
	}
}

func TestAuthorizeTool_WritesWithoutScopeFailClosed(t *testing.T) {
	setupAgentEnv(t, "")
	writeAgentDef(t, "author", `---
title: "Author"
tools: [docs]
---
Body.`)

	rt, err := buildAgentRuntime("author")
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	msg := rt.authorizeTool("write_doc", map[string]interface{}{"path": "new.mdx"})
	if !strings.Contains(msg, "write scope") {
		t.Errorf("write with no declared scope should fail closed, got %q", msg)
	}
}

func TestAuthorizeTool_ReadScopes(t *testing.T) {
	setupAgentEnv(t, "")
	writeAgentDef(t, "reader", `---
title: "Reader"
tools: [docs]
permissions:
  read: ["docs/**"]
---
Body.`)

	rt, err := buildAgentRuntime("reader")
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	if msg := rt.authorizeTool("read_doc", map[string]interface{}{"path": "index.mdx"}); msg != "" {
		t.Errorf("read_doc within scope should be allowed, got %q", msg)
	}
	if msg := rt.authorizeTool("read_doc", map[string]interface{}{"path": "roadmap.mdx"}); msg != "" {
		t.Errorf("read allowed for any docs/** target, got %q", msg)
	}

	// A read scope that excludes docs denies doc reads.
	writeAgentDef(t, "ticketreader", `---
title: "Ticket Reader"
tools: [docs]
permissions:
  read: ["tickets/**"]
---
Body.`)
	rt2, err := buildAgentRuntime("ticketreader")
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	if msg := rt2.authorizeTool("read_doc", map[string]interface{}{"path": "index.mdx"}); !strings.Contains(msg, "read scope") {
		t.Errorf("read_doc outside scope should be denied, got %q", msg)
	}

	// No declared read scope: reads are unrestricted.
	writeAgentDef(t, "open", `---
title: "Open"
tools: [docs]
---
Body.`)
	rt3, err := buildAgentRuntime("open")
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	if msg := rt3.authorizeTool("read_doc", map[string]interface{}{"path": "index.mdx"}); msg != "" {
		t.Errorf("read with no declared scope should be allowed, got %q", msg)
	}
}

func TestBuildAgentRuntime_PromptComposition(t *testing.T) {
	setupAgentEnv(t, "")
	writeSkillDef(t, "ste100", `---
title: "STe-100"
description: "Write in Simplified Technical English"
---
Use short sentences.`)

	writeAgentDef(t, "author", `---
title: "Author"
skills: [ste100]
permissions:
  write: ["docs/**"]
---
You are the author agent.`)

	rt, err := buildAgentRuntime("author")
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	for _, want := range []string{
		"You are the author agent.",
		"## Skill: STe-100",
		"Use short sentences.",
	} {
		if !strings.Contains(rt.prompt, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, rt.prompt)
		}
	}
}

func TestDispatchToolResolved_EnforcesRuntime(t *testing.T) {
	setupAgentEnv(t, "")
	writeAgentDef(t, "author", `---
title: "Author"
tools: [base]
permissions:
  write: ["tickets/**"]
---
Body.`)

	rt, err := buildAgentRuntime("author")
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}

	// Allowlisted tool runs.
	res := dispatchToolResolved(rt, "list_docs", map[string]interface{}{})
	if !strings.Contains(res, "[") {
		t.Errorf("list_docs should run, got %q", res)
	}

	// Tool outside the allowlist is denied before dispatch.
	content := "---\ntitle: New\n---\n\n# New doc"
	res = dispatchToolResolved(rt, "write_doc", map[string]interface{}{"path": "new.mdx", "content": content})
	if !strings.Contains(res, "not allowed") {
		t.Errorf("write_doc should be denied by allowlist, got %q", res)
	}

	// A write tool outside its scope is denied even when allowlisted.
	rt2, err := buildAgentRuntimeWithTools(t, "ticketderiver")
	if err != nil {
		t.Fatalf("buildAgentRuntimeWithTools: %v", err)
	}
	res = dispatchToolResolved(rt2, "write_doc", map[string]interface{}{"path": "new.mdx", "content": content})
	if !strings.Contains(res, "write scope") {
		t.Errorf("write_doc should be denied by scope, got %q", res)
	}
}

// buildAgentRuntimeWithTools writes a doc-only agent then builds it.
func buildAgentRuntimeWithTools(t *testing.T, slug string) (*agentRuntime, error) {
	t.Helper()
	writeAgentDef(t, slug, `---
title: "Ticket Deriver"
tools: [docs, tickets]
permissions:
  write: ["tickets/**"]
---
Body.`)
	return buildAgentRuntime(slug)
}

func TestResolveActivePrompt_NoFallback(t *testing.T) {
	setupAgentEnv(t, "docs")

	// No agent file deployed: the prompt is empty, never a built-in fallback.
	if prompt := resolveActivePrompt(); prompt != "" {
		t.Fatalf("expected no prompt without a deployed agent, got %q", prompt)
	}

	writeAgentDef(t, "docs", `---
title: "Docs"
---
You are the docs agent.`)
	if prompt := resolveActivePrompt(); !strings.Contains(prompt, "You are the docs agent.") {
		t.Fatalf("expected the deployed agent's prompt, got %q", prompt)
	}
}

func TestWriteArtifact_Tool(t *testing.T) {
	prev := DEVTOP_DIR
	prevCfg := engineConfig
	tempDir := setupTestDirs(t)
	DEVTOP_DIR = tempDir
	engineConfig = EngineConfig{ArtifactKinds: map[string]ArtifactKind{
		"prds":    {Path: "prds", Extension: ".mdx", AgentWritable: true},
		"tickets": {Path: "tickets", Extension: ".md", AgentWritable: true, View: "board"},
	}}
	t.Cleanup(func() {
		DEVTOP_DIR = prev
		engineConfig = prevCfg
		os.RemoveAll(tempDir)
	})

	res := dispatchTool("write_artifact", map[string]interface{}{"kind": "prds", "id": "architecture", "content": "---\ntitle: P\n---\n\nBody."})
	if !strings.Contains(res, "Written to") {
		t.Fatalf("write_artifact failed: %s", res)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "prds", "architecture", "index.mdx")); err != nil {
		t.Fatal("prd file not written")
	}

	res = dispatchTool("write_artifact", map[string]interface{}{"kind": "tickets", "id": "001", "content": "x"})
	if !strings.Contains(res, "typed write flow") {
		t.Errorf("board kind should be refused, got %q", res)
	}
	res = dispatchTool("write_artifact", map[string]interface{}{"kind": "bogus", "id": "x", "content": "x"})
	if !strings.Contains(res, "not an agent-writable") {
		t.Errorf("unknown kind should be refused, got %q", res)
	}
}

func TestWriteArtifact_RuntimeScope(t *testing.T) {
	setupAgentEnv(t, "")
	engineConfig.ArtifactKinds["docs"] = ArtifactKind{Path: "docs", Extension: ".mdx", AgentWritable: true}
	engineConfig.ArtifactKinds["prds"] = ArtifactKind{Path: "prds", Extension: ".mdx", AgentWritable: true}
	writeAgentDef(t, "prdbuilder", `---
title: "PRD Builder"
tools: [base, docs, write_artifact]
permissions:
  read: ["docs/**"]
  write: ["prds/**"]
---
Body.`)

	rt, err := buildAgentRuntime("prdbuilder")
	if err != nil {
		t.Fatalf("buildAgentRuntime: %v", err)
	}
	if msg := rt.authorizeTool("write_artifact", map[string]interface{}{"kind": "prds", "id": "architecture"}); msg != "" {
		t.Errorf("write_artifact into prds/** should be allowed, got %q", msg)
	}
	if msg := rt.authorizeTool("write_artifact", map[string]interface{}{"kind": "docs", "id": "architecture"}); !strings.Contains(msg, "write scope") {
		t.Errorf("write_artifact into docs/** should be denied for a prds writer, got %q", msg)
	}
}
