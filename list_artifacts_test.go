package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readWriteAgentRuntime(t *testing.T) *agentRuntime {
	t.Helper()
	return &agentRuntime{
		Slug:        "semantics-builder",
		allowlist:   map[string]bool{"read_artifact": true, "list_artifacts": true, "write_artifact": true},
		readScopes:  []string{"documentation/**"},
		writeScopes: []string{"requirements/**", "decisions/**", "open_questions/**"},
	}
}

func TestListArtifactsScopeAdmitsWriteKinds(t *testing.T) {
	// The path resolvers consult the global config for kind declarations.
	prevCfg := engineConfig
	cfg := prevCfg
	if cfg.ArtifactKinds == nil {
		cfg.ArtifactKinds = map[string]ArtifactKind{}
	}
	for k, kd := range map[string]ArtifactKind{
		"documentation":  {Path: "documentation", Extension: ".mdx"},
		"requirements":   {Path: "requirements", Extension: ".mdx"},
		"decisions":      {Path: "decisions", Extension: ".mdx"},
		"open_questions": {Path: "open_questions", Extension: ".mdx"},
		"intents":        {Path: "intents", Extension: ".mdx"},
	} {
		cfg.ArtifactKinds[k] = kd
	}
	engineConfig = cfg
	t.Cleanup(func() { engineConfig = prevCfg })

	// A semantics agent may survey any kind it writes to (the survey-then-
	// write loop), even when that kind is outside its configured read scope.
	rt := readWriteAgentRuntime(t)
	for _, kind := range []string{"requirements", "decisions", "open_questions"} {
		if msg := rt.authorizeTool("list_artifacts", map[string]interface{}{"kind": kind}); msg != "" {
			t.Errorf("list_artifacts(%s) denied: %s", kind, msg)
		}
	}
	// Its own read scope (documentation) is also surveyable.
	if msg := rt.authorizeTool("list_artifacts", map[string]interface{}{"kind": "documentation"}); msg != "" {
		t.Errorf("list_artifacts(documentation) denied: %s", msg)
	}
	// A kind it can neither read nor write stays denied — with the allowed
	// scopes named so the model can correct itself.
	msg := rt.authorizeTool("list_artifacts", map[string]interface{}{"kind": "intents"})
	if msg == "" {
		t.Error("list_artifacts(intents) should be denied for this agent")
	}
	if !strings.Contains(msg, "allowed:") || !strings.Contains(msg, "documentation/**") {
		t.Errorf("denial should name the allowed scopes, got %q", msg)
	}
	// Invalid kind args fail the path resolver, not a scope error.
	if msg := rt.authorizeTool("list_artifacts", map[string]interface{}{"kind": "../x"}); msg == "" {
		t.Error("list_artifacts(traversal) should fail the path resolver")
	}
}

func TestListArtifactsToolRoundTrip(t *testing.T) {
	resetGlobalLog(t, t.TempDir())
	repo := newTestRepo(t)

	// The throwaway repo has no config.yml, so pin the globals to a config
	// that declares the requirements kind, and register the repo so
	// defaultPaths() resolves to it.
	prevCfg := engineConfig
	cfg := prevCfg
	if cfg.ArtifactKinds == nil {
		cfg.ArtifactKinds = map[string]ArtifactKind{}
	}
	cfg.ArtifactKinds["requirements"] = ArtifactKind{Path: "requirements", Extension: ".mdx", AgentWritable: true}
	engineConfig = cfg
	registerWorkspaceRepoForTest(t, repo)
	prevDevtop := DEVTOP_DIR
	DEVTOP_DIR = repo.paths.DevTop
	t.Cleanup(func() {
		engineConfig = prevCfg
		DEVTOP_DIR = prevDevtop
	})

	writeArtifact := func(id, title string) {
		rel, _ := resolveArtifactWriteTargetFor(cfg, repo.paths, "requirements", id)
		full := filepath.Join(repo.paths.DevTop, rel)
		_ = os.MkdirAll(filepath.Dir(full), 0755)
		_ = os.WriteFile(full, []byte("---\ntitle: "+title+"\n---\nbody"), 0644)
	}
	writeArtifact("REQ-1", "Req One")
	writeArtifact("REQ-2", "Req Two")

	out := dispatchTool("list_artifacts", map[string]interface{}{"kind": "requirements"})
	if !strings.Contains(out, "REQ-1") || !strings.Contains(out, "Req One") || !strings.Contains(out, "REQ-2") {
		t.Fatalf("list_artifacts out = %q", out)
	}
	if strings.Contains(out, "Error listing") {
		t.Fatalf("list_artifacts returned an error: %q", out)
	}

	// An empty kind returns [], never null — models read null as an error.
	empty := dispatchTool("list_artifacts", map[string]interface{}{"kind": "decisions"})
	if strings.TrimSpace(empty) != "[]" {
		t.Fatalf("empty kind out = %q, want []", empty)
	}

	// read_artifact on a write kind now passes the read check, so the agent
	// can inspect what it is about to extend.
	rt := readWriteAgentRuntime(t)
	if msg := rt.authorizeTool("read_artifact", map[string]interface{}{"kind": "requirements", "id": "REQ-1"}); msg != "" {
		t.Errorf("read_artifact(requirements/REQ-1) denied: %s", msg)
	}
}
