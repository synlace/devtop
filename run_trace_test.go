package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func registeredRunRepo(t *testing.T) (*Repo, *Run) {
	t.Helper()
	resetGlobalLog(t, t.TempDir())
	cleanRegistry(t)
	repo := newTestRepo(t)
	registry.addLocked(repo)
	run := startRun(repo, "intents", "documentation", "INT-1", "describe_feature", "doc-builder", "medium", "prompt")
	return repo, run
}

func TestBuildRunTraceCompact(t *testing.T) {
	_, run := registeredRunRepo(t)
	run.ToolCall("read_artifact", "{}",
		map[string]interface{}{"kind": "intents", "id": "INT-1"}, "the intent content", 0)
	run.ToolCall("write_artifact", "{}",
		map[string]interface{}{"kind": "documentation", "content": "full doc"}, "Error: cannot determine target path", 0)
	run.Done()

	trace := buildRunTrace(run.Dir)
	for _, want := range []string{"Run run-", "intents", "read_artifact", "write_artifact", "describe_feature", "status done"} {
		if !strings.Contains(trace, want) {
			t.Errorf("trace missing %q:\n%s", want, trace)
		}
	}
	if !strings.Contains(trace, "Last error") {
		t.Errorf("trace lacks error section:\n%s", trace)
	}
}

func TestLatestRunningRunPreferred(t *testing.T) {
	resetGlobalLog(t, t.TempDir())
	cleanRegistry(t)
	repo := newTestRepo(t)
	registry.addLocked(repo)
	older := startRun(repo, "intents", "documentation", "INT-1", "describe_feature", "doc-builder", "", "p")
	older.Done()
	active := startRun(repo, "documentation", "requirements", "DOC-1", "derive_requirements", "semantics-builder", "", "p")

	// The newest completed run sits on disk, but the in-flight run wins for
	// the trace tool: that is the one needing help right now.
	out := runTraceToolHandler(map[string]interface{}{})
	if !strings.Contains(out, active.ID) || strings.Contains(out, older.ID) {
		t.Fatalf("trace tool picked the wrong run:\n%s", out)
	}
}

func TestHandleAPIRunsAndDetail(t *testing.T) {
	_, run := registeredRunRepo(t)
	run.Done()

	req := httptest.NewRequest("GET", "/api/runs", nil)
	req.Header.Set("X-Devtop-Repo", run.Repo.Name)
	rr := httptest.NewRecorder()
	handleAPIRuns(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var list struct {
		Runs []RunSummary `json:"runs"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.Runs) != 1 || list.Runs[0].ID != run.ID {
		t.Fatalf("runs list = %+v", list.Runs)
	}

	req = httptest.NewRequest("GET", "/api/runs/"+run.ID, nil)
	req.Header.Set("X-Devtop-Repo", run.Repo.Name)
	req.SetPathValue("id", run.ID)
	rr = httptest.NewRecorder()
	handleAPIRunDetail(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200", rr.Code)
	}
	var detail struct {
		Run     RunSummary               `json:"run"`
		Entries []map[string]interface{} `json:"entries"`
		Trace   string                   `json:"trace"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &detail)
	if detail.Run.ID != run.ID || len(detail.Entries) != 0 {
		t.Fatalf("detail = %+v", detail)
	}
	if !strings.Contains(detail.Trace, "run-") {
		t.Errorf("detail trace missing run header")
	}

	// Malformed ids are rejected.
	req = httptest.NewRequest("GET", "/api/runs/not-a-run", nil)
	req.SetPathValue("id", "../../etc/passwd")
	rr = httptest.NewRecorder()
	handleAPIRunDetail(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed id status = %d, want 400", rr.Code)
	}
}

func TestRunTraceToolHandlerProducesHint(t *testing.T) {
	_, run := registeredRunRepo(t)
	run.ToolCall("list_docs", "", map[string]interface{}{}, "[]", 0)
	run.Done()
	out := runTraceToolHandler(map[string]interface{}{})
	if !strings.Contains(out, "Next step") || !strings.Contains(out, "list_docs") {
		t.Fatalf("tool trace out = %q", out)
	}
	if strings.Contains(out, "no runs") {
		t.Fatalf("expected a real trace, got empty-state: %q", out)
	}
}
