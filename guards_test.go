package main

import (
	"strings"
	"testing"
	"time"
)

func newRunningRun(t *testing.T) *Run {
	t.Helper()
	resetGlobalLog(t, t.TempDir())
	repo := newTestRepo(t)
	return startRun(repo, "intents", "documentation", "INT-1", "describe_feature", "doc-builder", "", "prompt")
}

func callArgs() (string, map[string]interface{}) {
	return `{"kind":"documentation","id":"DOC-1"}`, map[string]interface{}{"kind": "documentation", "id": "DOC-1"}
}

func TestGuardLoopAbortsOnRepeat(t *testing.T) {
	run := newRunningRun(t)
	name, args := callArgs()
	for i := 0; i < 9; i++ {
		run.ToolCall(name, "", args, "ok", time.Millisecond)
	}
	if run.StatusNow() != "aborted" {
		t.Fatalf("status = %q, want aborted after %d identical calls", run.StatusNow(), 9)
	}
	if reason, _ := run.AbortReason(); !strings.Contains(reason, "identical arguments") {
		t.Errorf("abort reason = %q", reason)
	}
	// run.flag fired at the intermediate marks, run.aborted at the end.
	// Total events: started + 9 tool.called + 2 run.flag + 1 run.aborted.
	all := captureEvents(theLog, 0, 13)
	flags := 0
	aborted := 0
	for _, ev := range all {
		if ev.Type == "run.flag" {
			flags++
		}
		if ev.Type == "run.aborted" {
			aborted++
		}
	}
	if flags != 2 {
		t.Errorf("run.flag fired %d times, want 2 (at 3 and 6 repeats)", flags)
	}
	if aborted != 1 {
		t.Errorf("run.aborted fired %d times, want 1", aborted)
	}
}

func TestGuardErrorBudgetAborts(t *testing.T) {
	run := newRunningRun(t)
	name, args := callArgs()
	for i := 0; i < 5; i++ {
		run.ToolCall(name, "", args, "Error: cannot determine target path", time.Millisecond)
	}
	if run.StatusNow() != "aborted" {
		t.Fatalf("status = %q, want aborted after %d consecutive errors", run.StatusNow(), 5)
	}
	if reason, _ := run.AbortReason(); !strings.Contains(reason, "consecutive tool errors") {
		t.Errorf("abort reason = %q", reason)
	}
	// A success between errors resets the budget.
	run2 := newRunningRun(t)
	for i := 0; i < 2; i++ {
		run2.ToolCall(name, "", args, "Error: x", time.Millisecond)
	}
	run2.ToolCall(name, "", args, "Written ok", time.Millisecond)
	for i := 0; i < 4; i++ {
		run2.ToolCall(name, "", args, "Error: x", time.Millisecond)
	}
	if run2.StatusNow() != "running" {
		t.Fatalf("budget did not reset on success: %q", run2.StatusNow())
	}
}

func TestGuardGitCommitFailuresExemptFromBudget(t *testing.T) {
	run := newRunningRun(t)
	// git_commit failing every time (no-git repo) must never abort the run
	// via the error budget. Stay under the loop-guard threshold (8 identical
	// calls) — the loop guard is a separate concern and still applies.
	for i := 0; i < 7; i++ {
		run.ToolCall("git_commit", "", map[string]interface{}{"message": "m"}, "Error: not a git repository at /x — commits are disabled", time.Millisecond)
	}
	if run.StatusNow() != "running" {
		t.Fatalf("git_commit failures aborted the run: %q", run.StatusNow())
	}
	// And a real write still succeeds and counts.
	run.ToolCall("write_artifact", "", map[string]interface{}{"kind": "documentation", "content": "x"}, "Written to .devtop/documentation/DOC-1 (id=DOC-1)", 0)
	if run.Writes() != 1 {
		t.Fatalf("writes = %d, want 1", run.Writes())
	}
	// git_commit never counts as a write either.
	if run.Writes() != 1 {
		t.Fatalf("git_commit counted as a write: %d", run.Writes())
	}
}

func TestGuardTouchKeepsRunAlive(t *testing.T) {
	run := newRunningRun(t)
	run.mu.Lock()
	run.last = time.Now().Add(-idleAbort - time.Minute)
	run.mu.Unlock()
	run.Touch()
	run.mu.Lock()
	idle := time.Since(run.last)
	run.mu.Unlock()
	if idle > idleAbort {
		t.Fatalf("Touch did not refresh activity: %v", idle)
	}
}
