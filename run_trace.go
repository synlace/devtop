package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// run_trace: the agent-facing debugging window onto its own run, plus the
// human-facing GET /api/runs endpoints.
//
// Every derive writes data/runs/<id>/meta.json (the inputs: prompt hash,
// edge, agent) and appends data/runs/<id>/agent.jsonl (one compact line per
// tool call). run_trace reads those files into a short, token-cheap digest —
// no DB, works without git, and survives restarts.

func init() {
	registerTool("run_trace", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "run_trace",
			"description": "Show the debugging trace of the current derive run: tool calls, results, errors, and warnings. Call this whenever a tool call fails, you are about to repeat a call, or you feel stuck — the trace shows what has happened and what to do next.",
			"parameters": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}, runTraceToolHandler)
}

var runsIDRe = regexp.MustCompile(`^run-[0-9a-f]{4,16}$`)

func runsDirFor(repo *Repo) string {
	return filepath.Join(repo.paths.DevTop, "data", "runs")
}

// RunSummary is the JSON shape of data/runs/<id>/meta.json.
type RunSummary struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Repo        string `json:"repo,omitempty"`
	StartedAt   string `json:"started_at"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	Slug        string `json:"slug,omitempty"`
	Transform   string `json:"transform,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Sensitivity string `json:"sensitivity,omitempty"`
	PromptHash  string `json:"prompt_hash,omitempty"`
	FromSHA     string `json:"from_sha,omitempty"`
}

func readRunMeta(dir string) (RunSummary, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return RunSummary{}, false
	}
	var s RunSummary
	if json.Unmarshal(b, &s) != nil {
		return RunSummary{}, false
	}
	if s.ID == "" {
		s.ID = filepath.Base(dir)
	}
	if s.Status == "" {
		s.Status = "unknown"
	}
	return s, true
}

func readRunLines(dir string) []map[string]interface{} {
	f, err := os.Open(filepath.Join(dir, "agent.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]interface{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		var rec map[string]interface{}
		if json.Unmarshal(sc.Bytes(), &rec) == nil {
			out = append(out, rec)
		}
	}
	return out
}

// buildRunTrace composes the compact, human-readable trace for one run dir.
func buildRunTrace(dir string) string {
	meta, ok := readRunMeta(dir)
	if !ok {
		return fmt.Sprintf("No run trace found in %s (was the run dir removed?)", dir)
	}
	return buildRunTraceFrom(meta, readRunLines(dir))
}

func buildRunTraceFrom(meta RunSummary, entries []map[string]interface{}) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %s · %s → %s · %s\n", meta.ID, meta.From, meta.To, meta.Agent)
	if meta.Transform != "" {
		fmt.Fprintf(&b, "transform: %s", meta.Transform)
	}
	if meta.Sensitivity != "" {
		fmt.Fprintf(&b, " · clarify: %s", meta.Sensitivity)
	}
	fmt.Fprintf(&b, "\nstarted %s · status %s\n", meta.StartedAt, meta.Status)

	repeat := map[string]int{}
	var errs []string
	for i, rec := range entries {
		name, _ := rec["name"].(string)
		args, _ := rec["args"].(map[string]interface{})
		ok, _ := rec["ok"].(bool)
		ms, _ := rec["duration_ms"].(float64)
		resHead, _ := rec["result_head"].(string)
		var keys []string
		for k := range args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var argStr []string
		for _, k := range keys {
			v, _ := args[k].(string)
			argStr = append(argStr, fmt.Sprintf("%s=%s", k, v))
		}
		marker := "ok"
		if !ok {
			marker = "ERROR"
			if len(errs) < 3 {
				errs = append(errs, resHead)
			}
		}
		key := name + "|" + strings.Join(argStr, " ")
		repeat[key]++
		fmt.Fprintf(&b, "%2d. %s(%s) · %s", i+1, name, strings.Join(argStr, " "), marker)
		if ms > 0 {
			fmt.Fprintf(&b, " · %dms", int64(ms))
		}
		b.WriteByte('\n')
	}

	var flagged []string
	for k, n := range repeat {
		if n >= loopFlagAt {
			flagged = append(flagged, fmt.Sprintf("%d× %s", n, k))
		}
	}
	if len(errs) > 0 {
		fmt.Fprintf(&b, "\nLast error(s):\n")
		for _, e := range errs {
			fmt.Fprintf(&b, "  ! %s\n", head(e, 160))
		}
	}
	if len(flagged) > 0 {
		sort.Strings(flagged)
		fmt.Fprintf(&b, "\nRepeated calls (possible loop):\n")
		for _, f := range flagged {
			fmt.Fprintf(&b, "  ~ %s\n", f)
		}
	}
	fmt.Fprintf(&b, "\nNext step: re-read the source artifact with read_artifact, then write each derived artifact exactly once. If a call failed twice, run_trace again before retrying; if you are stuck after 3 identical calls, stop and state the blocker instead.\n")
	return strings.TrimRight(b.String(), "\n")
}

// runTraceToolHandler shows the repo's current or most recent run trace.
func runTraceToolHandler(args map[string]interface{}) string {
	repo := forRepoOf(toolPaths())
	active := activeRunsFor(repo)
	if len(active) > 0 {
		sort.Slice(active, func(i, j int) bool { return active[i].StartedAt.After(active[j].StartedAt) })
		return buildRunTrace(active[0].Dir)
	}
	if dir := latestRunDir(repo); dir != "" {
		return buildRunTrace(dir)
	}
	return "No runs recorded for this repo yet — runs appear after the first derive. If you hit an error before a run started, report it as the tool result and move on."
}

func latestRunDir(repo *Repo) string {
	entries, err := os.ReadDir(runsDirFor(repo))
	if err != nil {
		return ""
	}
	var latest time.Time
	var dir string
	for _, e := range entries {
		d := filepath.Join(runsDirFor(repo), e.Name())
		if fi, err := e.Info(); err == nil && fi.ModTime().After(latest) {
			latest = fi.ModTime()
			dir = d
		}
	}
	return dir
}

// handleAPIRuns lists the repo's runs, newest first (mirrors + live status).
//
//	GET /api/runs
func handleAPIRuns(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	entries, err := os.ReadDir(runsDirFor(repo))
	if err != nil {
		entries = nil
	}
	var out []RunSummary
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "run-") {
			continue
		}
		if s, ok := readRunMeta(filepath.Join(runsDirFor(repo), e.Name())); ok {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	if len(out) > 50 {
		out = out[:50]
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"runs": out})
}

// handleAPIRunDetail serves the full run: meta, mirror entries, and the
// generated trace text.
//
//	GET /api/runs/{id}
func handleAPIRunDetail(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !runsIDRe.MatchString(id) {
		http.Error(w, "invalid run id", 400)
		return
	}
	dir := filepath.Join(runsDirFor(repo), id)
	meta, ok := readRunMeta(dir)
	if !ok {
		http.NotFound(w, r)
		return
	}
	entries := readRunLines(dir)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"run":     meta,
		"entries": entries,
		"trace":   buildRunTraceFrom(meta, entries),
		"mirror":  "data/runs/" + id + "/agent.jsonl",
	})
}
