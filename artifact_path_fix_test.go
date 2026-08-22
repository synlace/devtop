package main

import (
	"strings"
	"testing"
)

func TestPreviewMintMatchesRealMint(t *testing.T) {
	resetGlobalLog(t, t.TempDir())
	repo := newTestRepo(t)
	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("config: %v", err)
	}

	// Preview first, then mint: the preview must equal the minted id and
	// never consume (the counter advances only via the real mint).
	for _, want := range []string{"DOC-1", "DOC-2", "DOC-3"} {
		preview := previewMintArtifactID(cfg, repo.paths, "documentation")
		if preview != want {
			t.Fatalf("preview #%s = %q", want, preview)
		}
		got := mintArtifactID(cfg, repo.paths, "documentation")
		if got != want {
			t.Fatalf("mint = %q, want %q (preview was %q)", got, want, preview)
		}
	}
	// A preview after the mints sees the next id — nothing was consumed.
	if got := previewMintArtifactID(cfg, repo.paths, "documentation"); got != "DOC-4" {
		t.Fatalf("post-mint preview = %q, want DOC-4", got)
	}
}

// registerWorkspaceRepoForTest pins the repo as the single registered
// project so defaultPaths()/engineConfig resolve to it.
func registerWorkspaceRepoForTest(t *testing.T, repo *Repo) {
	t.Helper()
	t.Setenv("DEVTOP_REPOS_FILE", t.TempDir()+"/repos.json")
	resetRegistry()
	registry.addLocked(repo)
	t.Cleanup(resetRegistry)
}

func TestArtifactToolPathEmptyIDResolves(t *testing.T) {
	resetGlobalLog(t, t.TempDir())
	repo := newTestRepo(t)
	registerWorkspaceRepoForTest(t, repo)
	// artifactToolPath resolves through the global config (authorization runs
	// before the repo pin), so pin the globals to the test repo's config.
	prevCfg := engineConfig
	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	engineConfig = cfg
	t.Cleanup(func() { engineConfig = prevCfg })

	rel, ok := artifactToolPath(map[string]interface{}{"kind": "documentation"})
	if !ok {
		t.Fatalf("empty-id write_artifact must resolve, got (_, false)")
	}
	if !strings.HasPrefix(rel, "documentation/DOC-") {
		t.Fatalf("resolved path = %q, want documentation/DOC-*", rel)
	}
	// Kind-level write scope must accept the minted path.
	rt := &agentRuntime{allowlist: map[string]bool{"write_artifact": true}, writeScopes: []string{"documentation/**"}}
	if msg := rt.authorizeTool("write_artifact", map[string]interface{}{"kind": "documentation"}); msg != "" {
		t.Fatalf("authorization denied the minted path: %s", msg)
	}
}

func TestRunWritesCounter(t *testing.T) {
	run := newRunningRun(t)
	name := "write_artifact"
	args := map[string]interface{}{"kind": "documentation", "content": "x"}
	run.ToolCall(name, "", args, "Written to .devtop/documentation/DOC-1 (id=DOC-1)", 0)
	if run.Writes() != 1 {
		t.Fatalf("writes = %d, want 1", run.Writes())
	}
	// A failed write does not count.
	run.ToolCall(name, "", args, "Error: cannot determine target path", 0)
	if run.Writes() != 1 {
		t.Fatalf("writes = %d after failure, want 1", run.Writes())
	}
	// Reads never count.
	run.ToolCall("read_artifact", "", map[string]interface{}{"kind": "intents", "id": "INT-1"}, "content", 0)
	if run.Writes() != 1 {
		t.Fatalf("writes = %d after read, want 1", run.Writes())
	}
}
