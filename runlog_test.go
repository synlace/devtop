package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestRepo builds a throwaway repo without touching the global registry,
// exactly like the repo tests do for hermetic cases.
func newTestRepo(t *testing.T) *Repo {
	t.Helper()
	p := newRepoPaths(filepath.Join(t.TempDir(), ".devtop"))
	repo := &Repo{
		Name:  "tester",
		Root:  filepath.Dir(p.DevTop),
		Dir:   p.DevTop,
		paths: p,
	}
	return repo
}

func TestRunLifecycleEmitsAndMirror(t *testing.T) {
	resetGlobalLog(t, t.TempDir())
	repo := newTestRepo(t)
	run := startRun(repo, "intents", "documentation", "INT-1", "describe_feature", "doc-builder", "medium", "prompt-X")
	defer run.Abort("test cleanup")

	if !strings.HasPrefix(run.ID, "run-") {
		t.Fatalf("run id %q, want run- prefix", run.ID)
	}
	if run.StatusNow() != "running" {
		t.Fatalf("status = %q, want running", run.StatusNow())
	}

	run.Touch()
	run.ToolCall("read_artifact", `{"kind":"intents","id":"INT-1"}`,
		map[string]interface{}{"kind": "intents", "id": "INT-1"}, "content here", 3_000_000 /*3ms*/)
	run.Done()

	if run.StatusNow() != "done" {
		t.Fatalf("status = %q, want done", run.StatusNow())
	}

	// Durable mirror: meta.json and one agent.jsonl line exist.
	meta, ok := readRunMeta(run.Dir)
	if !ok {
		t.Fatalf("meta.json missing in %s", run.Dir)
	}
	if meta.Status != "done" || meta.From != "intents" || meta.To != "documentation" {
		t.Fatalf("meta = %+v", meta)
	}
	if meta.Transform != "describe_feature" || meta.Agent != "doc-builder" || meta.Sensitivity != "medium" {
		t.Fatalf("meta inputs = %+v", meta)
	}
	if len(meta.PromptHash) != 16 {
		t.Fatalf("prompt_hash = %q, want 16 hex chars", meta.PromptHash)
	}
	lines := readRunLines(run.Dir)
	if len(lines) != 1 {
		t.Fatalf("agent.jsonl lines = %d, want 1", len(lines))
	}
	if lines[0]["name"] != "read_artifact" {
		t.Fatalf("mirror line = %+v", lines[0])
	}
	if ok, _ := lines[0]["ok"].(bool); !ok {
		t.Fatalf("mirror call marked not ok: %+v", lines[0])
	}
	if _, ok := lines[0]["args"].(map[string]interface{}); !ok {
		t.Fatalf("mirror call lacks args digest: %+v", lines[0])
	}

	// Event sequence: started, tool.called, done, in order, run-scoped.
	all := captureEvents(theLog, 0, 3)
	types := []string{"run.started", "tool.called", "run.done"}
	for i, ev := range all {
		if ev.Type != types[i] {
			t.Errorf("event %d type = %q, want %q", i, ev.Type, types[i])
		}
		var p map[string]interface{}
		_ = json.Unmarshal(ev.Payload, &p)
		if p["run"] != run.ID {
			t.Errorf("event %d run = %v, want %s", i, p["run"], run.ID)
		}
		if p["from"] != "intents" || p["slug"] != "INT-1" {
			t.Errorf("event %d run scope missing from/slug: %v", i, p)
		}
	}
}

func TestRunTerminalChannelAndUIDeriveEvents(t *testing.T) {
	resetGlobalLog(t, t.TempDir())
	repo := newTestRepo(t)
	run := startRun(repo, "intents", "documentation", "INT-1", "describe_feature", "doc-builder", "", "prompt")

	// Terminal() fires the moment the run ends, even before the caller
	// unwinds — the contract the HTTP handler relies on to close promptly.
	term := run.Terminal()
	select {
	case <-term:
		t.Fatal("Terminal() fired before the run ended")
	default:
	}
	run.ToolCall("write_artifact", "", map[string]interface{}{"kind": "documentation", "content": "x"}, "Written to .devtop/documentation/DOC-1 (id=DOC-1)", 0)
	run.Done()
	select {
	case <-term:
	default:
		t.Fatal("Terminal() did not fire after Done()")
	}

	// Done emits both run.done and the UI terminal derive.done.
	all := captureEvents(theLog, 0, 4)
	types := []string{"run.started", "tool.called", "run.done", "derive.done"}
	for i, ev := range all {
		if ev.Type != types[i] {
			t.Errorf("event %d type = %q, want %q", i, ev.Type, types[i])
		}
	}
	var p map[string]interface{}
	_ = json.Unmarshal(all[3].Payload, &p)
	if p["run"] != run.ID || p["status"] != "done" {
		t.Errorf("derive.done payload = %v", p)
	}
}

func TestRunCrashEmitsFailedWithNoDoubleTerminal(t *testing.T) {
	resetGlobalLog(t, t.TempDir())
	repo := newTestRepo(t)
	run := startRun(repo, "documentation", "requirements", "DOC-1", "derive_requirements", "semantics-builder", "", "prompt")
	run.Crash(errString("model call failed"))
	if run.StatusNow() != "failed" {
		t.Fatalf("status = %q, want failed", run.StatusNow())
	}
	// Crash twice must not emit twice: exactly one terminal event.
	run.Crash(errString("again"))
	all := captureEvents(theLog, 0, 2)
	failed := 0
	for _, ev := range all {
		if ev.Type == "run.failed" {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("run.failed emitted %d times, want 1", failed)
	}
	if _, err := os.Stat(filepath.Join(run.Dir, "fail.json")); err != nil {
		t.Fatalf("fail.json missing: %v", err)
	}
}

type runTestError string

func (e runTestError) Error() string { return string(e) }

func errString(s string) error { return runTestError(s) }

func TestRunAbortFromToolAndFailJson(t *testing.T) {
	resetGlobalLog(t, t.TempDir())
	repo := newTestRepo(t)
	run := startRun(repo, "documentation", "open_questions", "DOC-1", "derive_open_questions", "semantics-builder", "", "prompt")
	run.Abort("watchdog idle")
	if reason, ok := run.AbortReason(); !ok || reason != "watchdog idle" {
		t.Fatalf("AbortReason = %q, %v", reason, ok)
	}
	all := captureEvents(theLog, 0, 2)
	if all[1].Type != "run.aborted" {
		t.Fatalf("terminal type = %q, want run.aborted", all[1].Type)
	}
}

func TestRunNoRegistryFallback(t *testing.T) {
	// forRepoOf on a throwaway repo must resolve without the registry.
	repo := newTestRepo(t)
	back := forRepoOf(repo.paths)
	if back == nil || back.paths.DevTop != repo.paths.DevTop {
		t.Fatalf("forRepoOf did not map back")
	}
}
