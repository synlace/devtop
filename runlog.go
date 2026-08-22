package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Run lifecycle and the per-run durable trace.
//
// Every derive (and any future agent run) is a Run: it allocates a run id,
// records its inputs — prompt hash, edge, agent, sensitivity — in
// data/runs/<id>/meta.json, mirrors every tool call to
// data/runs/<id>/agent.jsonl, and emits run.started/run.done/run.failed and
// run.aborted events on the durable log. The mirror is git-independent: a
// repo without commits still keeps a complete trace. A Run is the debugging
// unit agents and humans share: agents call run_trace, humans read
// GET /api/runs/<id> or .devtop/data/runs/<id>.
//
// Every run reaches exactly one terminal event, even when the model fails,
// the run loops, or the server watchdog aborts it.

// startRun creates and starts a run bound to a repo. The returned Run owns a
// cancelable context that the watchdog uses to stop a stalled agent mid-run.
func startRun(repo *Repo, from, to, slug, transform, agent, sensitivity, prompt string) *Run {
	id := fmt.Sprintf("run-%s", randHex(4))
	dir := filepath.Join(repo.paths.DevTop, "data", "runs", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// A missing runs dir must never silently drop the trace. The event
		// log stays the durable spine either way.
		fmt.Printf("Warning: cannot create run dir %s: %v\n", dir, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Run{
		ID:          id,
		Repo:        repo,
		Dir:         dir,
		Status:      "running",
		From:        from,
		To:          to,
		Slug:        slug,
		Transform:   transform,
		Agent:       agent,
		Sensitivity: sensitivity,
		PromptHash:  promptHashOf(prompt),
		StartedAt:   time.Now(),
		ctx:         ctx,
		cancel:      cancel,
		steps:       map[string]int{},
		terminalCh:  make(chan struct{}),
	}
	r.runCtx = context.WithValue(ctx, runCtxKey{}, r)
	if f, err := os.OpenFile(filepath.Join(dir, "agent.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		r.agentLog = f
	} else {
		fmt.Printf("Warning: cannot open run mirror %s: %v\n", dir, err)
	}
	r.writeMeta()
	r.emit("run.started", map[string]interface{}{"status": "running"})
	registerRun(r)
	ensureWatchdog()
	return r
}

// Run is one agent run: its identity, its inputs, and the durable mirror.
type Run struct {
	ID          string
	Repo        *Repo
	Dir         string // .devtop/data/runs/<id>
	Status      string // running | done | failed | aborted
	From        string
	To          string
	Slug        string
	Transform   string
	Agent       string
	Sensitivity string
	PromptHash  string
	StartedAt   time.Time

	ctx    context.Context
	cancel context.CancelFunc
	runCtx context.Context

	mu          sync.Mutex
	fileMu      sync.Mutex
	last        time.Time
	steps       map[string]int // loop guard: tool+canonical-args -> call count
	errs        int            // consecutive failed tool calls
	writes      int            // successful artifact/ticket writes
	terminal    bool
	terminalCh  chan struct{} // closed exactly once when the run terminates
	abortReason string
	agentLog    *os.File
}

// runCtxKey carries the active Run through the agent loop's context.
type runCtxKey struct{}

// Ctx returns the run-bound context: caller cancellation plus a marker the
// agent loop reads to attribute tool calls to this run.
func (r *Run) Ctx() context.Context { return r.runCtx }

// CurrentRun extracts the run from an agent-loop context, or nil.
func CurrentRun(ctx context.Context) *Run {
	r, _ := ctx.Value(runCtxKey{}).(*Run)
	return r
}

// Touch marks activity so the watchdog never aborts a live run.
func (r *Run) Touch() {
	r.mu.Lock()
	r.last = time.Now()
	r.mu.Unlock()
}

// ToolCall records one dispatched tool call: durable mirror line, telemetry
// event, loop guard, and consecutive-error budget.
func (r *Run) ToolCall(name, argsRaw string, args map[string]interface{}, result string, d time.Duration) {
	digest := toolArgsDigest(args)
	dkey := name + "|" + mustJSON(digest)
	ok := !isToolError(result)
	wrote := ok && writeToolsTracked[name]
	r.mu.Lock()
	r.last = time.Now()
	r.steps[dkey]++
	count := r.steps[dkey]
	if ok {
		r.errs = 0
	} else if !budgetExemptTools[name] {
		r.errs++
	}
	if wrote {
		r.writes++
	}
	consecErr := r.errs
	r.mu.Unlock()

	r.appendLogLine(map[string]interface{}{
		"type": "tool", "name": name, "args": digest,
		"result_head": head(result, 200), "ok": ok,
		"duration_ms": d.Milliseconds(),
	})
	r.emit("tool.called", map[string]interface{}{
		"name": name, "args": digest, "ok": ok,
		"duration_ms": d.Milliseconds(),
	})

	if r.terminalStatus() {
		return
	}
	// Loop guard: the same tool call repeated beats is the flail signature.
	if count >= loopAbortRepeats {
		r.Abort(fmt.Sprintf("called %s with identical arguments %d times", name, count))
		return
	}
	if count == loopFlagAt || count == loopFlagAgain {
		r.emit("run.flag", map[string]interface{}{
			"kind": "loop", "tool": name,
			"detail": fmt.Sprintf("%s called %d times with identical arguments", name, count),
		})
	}
	// Error budget: consecutive failures with no successful step in between.
	// git_commit failures are environmental and never budgeted.
	if !ok && consecErr >= errorBudget && !budgetExemptTools[name] {
		r.Abort(fmt.Sprintf("%d consecutive tool errors — the run cannot proceed", consecErr))
	}
}

// writeToolsTracked are the tool names whose success means the run produced
// a durable artifact. A run whose model loop "succeeds" without one of these
// is a failure, not a done. git_commit is deliberately absent: it is a
// bookkeeping call, not a content write, and in a repo without git it fails
// every time — it must never count toward the error budget either.
var writeToolsTracked = map[string]bool{
	"write_artifact": true,
	"write_doc":      true,
	"create_ticket":  true,
	"update_ticket":  true,
	"add_comment":    true,
}

// budgetExemptTools never count toward the consecutive-error budget: they
// fail for environmental reasons the model cannot fix (e.g. git_commit in a
// repo without git), so a run must not abort because the environment is
// uncooperative.
var budgetExemptTools = map[string]bool{
	"git_commit": true,
}

// Writes returns the number of successful tracked writes so far.
func (r *Run) Writes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writes
}

// Done marks a successful run. Exactly one terminal event per run. The UI
// terminal event (derive.done) is emitted here, at the moment the run ends —
// never by the HTTP handler, which may still be unwinding a slow agent
// goroutine.
func (r *Run) Done() {
	if !r.setTerminal("done") {
		return
	}
	r.closeLog()
	r.writeMeta()
	r.emit("run.done", map[string]interface{}{"status": "done"})
	r.emit("derive.done", map[string]interface{}{"status": "done"})
	unregisterRun(r)
}

// Crash marks a failed run from an agent-level error (model call, max depth).
func (r *Run) Crash(agentErr error) {
	if !r.setTerminal("failed") {
		return
	}
	r.cancel()
	r.closeLog()
	r.writeFail("agent_failure", agentErr.Error(), "")
	r.writeMeta()
	r.emit("run.failed", map[string]interface{}{"status": "failed", "error": agentErr.Error()})
	r.emit("derive.aborted", map[string]interface{}{"status": "failed", "error": agentErr.Error()})
	unregisterRun(r)
}

// Abort stops a run from a guard: watchdog idle, loop, or error budget.
func (r *Run) Abort(reason string) {
	r.mu.Lock()
	if r.terminal {
		r.mu.Unlock()
		return
	}
	r.terminal = true
	r.Status = "aborted"
	r.abortReason = reason
	close(r.terminalCh)
	r.mu.Unlock()
	r.cancel()
	r.closeLog()
	r.writeFail("aborted", reason, "")
	r.writeMeta()
	r.emit("run.aborted", map[string]interface{}{"status": "aborted", "reason": reason})
	r.emit("derive.aborted", map[string]interface{}{"status": "aborted", "reason": reason})
	unregisterRun(r)
}

// Terminal returns a channel closed the moment the run reaches a terminal
// state. HTTP handlers select on it so a response closes promptly even when
// the agent goroutine is stuck unwinding a model stream.
func (r *Run) Terminal() <-chan struct{} {
	return r.terminalCh
}

// AbortReason returns the guard reason of an aborted run.
func (r *Run) AbortReason() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abortReason, r.terminal && r.Status == "aborted"
}

// StatusNow reads the current status under the lock.
func (r *Run) StatusNow() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Status
}

func (r *Run) terminalStatus() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.terminal
}

func (r *Run) setTerminal(status string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal {
		return false
	}
	r.terminal = true
	r.Status = status
	close(r.terminalCh)
	return true
}

func (r *Run) emit(typ string, payload map[string]interface{}) {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	payload["run"] = r.ID
	payload["from"] = r.From
	payload["slug"] = r.Slug
	emitRunEvent(typ, r.To, payload)
}

func (r *Run) appendLogLine(rec map[string]interface{}) {
	r.fileMu.Lock()
	defer r.fileMu.Unlock()
	if r.agentLog == nil {
		return
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(r.agentLog, "%s\n", b)
}

func (r *Run) closeLog() {
	r.fileMu.Lock()
	defer r.fileMu.Unlock()
	if r.agentLog != nil {
		_ = r.agentLog.Close()
		r.agentLog = nil
	}
}

func (r *Run) writeMeta() {
	r.mu.Lock()
	status := r.Status
	r.mu.Unlock()
	meta := map[string]interface{}{
		"id":          r.ID,
		"status":      status,
		"repo":        r.Repo.Name,
		"started_at":  r.StartedAt.UTC().Format(time.RFC3339),
		"from":        r.From,
		"to":          r.To,
		"slug":        r.Slug,
		"transform":   r.Transform,
		"agent":       r.Agent,
		"sensitivity": r.Sensitivity,
		"prompt_hash": r.PromptHash,
		"from_sha":    repoHeadSHA(r.Repo),
	}
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(r.Dir, "meta.json"), b, 0o644)
}

func (r *Run) writeFail(kind, detail, tool string) {
	rec := map[string]interface{}{
		"kind": kind, "detail": detail, "run": r.ID,
		"at": time.Now().UTC().Format(time.RFC3339),
	}
	if tool != "" {
		rec["tool"] = tool
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(r.Dir, "fail.json"), b, 0o644)
}

// activeRuns tracks in-flight runs for the watchdog and the trace tool.
var activeRuns = struct {
	mu sync.Mutex
	m  map[string]*Run
}{m: map[string]*Run{}}

func registerRun(r *Run) {
	activeRuns.mu.Lock()
	activeRuns.m[r.ID] = r
	activeRuns.mu.Unlock()
}

func unregisterRun(r *Run) {
	activeRuns.mu.Lock()
	delete(activeRuns.m, r.ID)
	activeRuns.mu.Unlock()
}

// activeRunsFor returns a snapshot of the given repo's active runs, newest
// first.
func activeRunsFor(repo *Repo) []*Run {
	activeRuns.mu.Lock()
	defer activeRuns.mu.Unlock()
	var out []*Run
	for _, r := range activeRuns.m {
		if r.Repo == repo {
			out = append(out, r)
		}
	}
	return out
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func promptHashOf(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:8])
}

func head(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n])
}

func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// isToolError reports whether a tool result is an error. Engine-authored
// errors start with "Error" or "error"; sandbox failures do too.
func isToolError(result string) bool {
	head := strings.TrimSpace(head(result, 16))
	return strings.HasPrefix(head, "Error") || strings.HasPrefix(head, "error")
}
