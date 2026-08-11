package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
)

// handleAPIInternalTool executes one agent tool scoped to the repo named by
// X-Devtop-Repo (empty = the default repo). The CopilotKit chat runtime
// delegates its tools here so there is exactly one enforcement point for path
// containment. On Linux with Landlock, the tool runs in a sandboxed
// subprocess that cannot write outside the repo root.
func handleAPIInternalTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string                 `json:"name"`
		Args map[string]interface{} `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Invalid body")
		return
	}
	name := strings.TrimSpace(r.Header.Get("X-Devtop-Repo"))
	if name == "" {
		name = r.URL.Query().Get("repo")
	}
	repo, err := registry.Resolve(name)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	result := dispatchRepoTool(repo, req.Name, req.Args)
	// Auto-assess: a doc the chat agent writes is handed to its classifier
	// agent right after the write lands, so agent-written docs are eligible
	// or not without a manual click. assessArtifact swallows every failure.
	if k, slug, ok := assessmentTarget(repo, req.Name, req.Args); ok && !strings.HasPrefix(result, "Error:") {
		go assessArtifact(repo, k, slug)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"result": result})
}

// sandboxEnabled reports whether tool calls should run in the Landlock
// sandbox. Set DEVTOP_TOOL_SANDBOX=0 to force in-process dispatch.
func sandboxEnabled() bool {
	return os.Getenv("DEVTOP_TOOL_SANDBOX") != "0"
}

var sandboxWarned atomic.Bool

// dispatchRepoTool runs a tool scoped to a repo, sandboxed when possible.
// The chat is always the repo's default agent: tool calls are authorized
// against that agent's allowlist and permission scopes BEFORE dispatch, with
// no fallback — a repo without its default agent deployed is refused. If the
// sandbox is unavailable (old kernel, non-Linux, test binary), it falls back
// to in-process dispatch, which still enforces the guardPath containment
// checks.
func dispatchRepoTool(repo *Repo, name string, args map[string]interface{}) string {
	rt := activeRuntimeFor(repo)
	if repo != nil && !zeroRepoInstance() && rt == nil {
		return fmt.Sprintf("Error: no agent configured: initialize the repo to scaffold .devtop/agents")
	}
	if rt != nil {
		if msg := rt.authorizeTool(name, args); msg != "" {
			return msg
		}
	}
	if sandboxEnabled() {
		if out, err := runToolSandboxed(repo, name, args); err == nil {
			return out
		} else if sandboxWarned.CompareAndSwap(false, true) {
			fmt.Fprintf(os.Stderr, "devtop: tool sandbox unavailable (%v) — falling back to in-process containment\n", err)
		}
	}
	return dispatchToolForRepo(repo, nil, name, args)
}

// runToolSandboxed re-executes this binary as `devtop-bin tool-run` under a
// Landlock sandbox that allows writes only under the repo root.
func runToolSandboxed(repo *Repo, name string, args map[string]interface{}) (string, error) {
	if repo == nil {
		return "", fmt.Errorf("no repo for sandboxed tool call")
	}
	if strings.HasSuffix(os.Args[0], ".test") {
		return "", fmt.Errorf("sandbox disabled in test binaries")
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	cmd := exec.Command(os.Args[0], "tool-run")
	cmd.Env = append(os.Environ(),
		"DEVTOP_TOOL_SANDBOX_RUN=1",
		"DEVTOP_SANDBOX_ROOT="+repo.Root,
		"DEVTOP_TOOL_NAME="+name,
		"DEVTOP_TOOL_ARGS="+string(argsJSON),
	)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runToolSubprocess is the tool-run entry point, handed off to from main()
// before any server state is built. It applies the sandbox, dispatches one
// tool, and prints the result on stdout.
func runToolSubprocess() int {
	root := os.Getenv("DEVTOP_SANDBOX_ROOT")
	name := os.Getenv("DEVTOP_TOOL_NAME")
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(os.Getenv("DEVTOP_TOOL_ARGS")), &args); err != nil {
		fmt.Fprintln(os.Stderr, "tool-run: bad args:", err)
		return 1
	}
	if err := applyLandlock(root, "/dev", os.TempDir()); err != nil {
		fmt.Fprintln(os.Stderr, "tool-run: sandbox failed:", err)
		return 2
	}
	fmt.Print(dispatchToolForRepo(newRepo(root), nil, name, args))
	return 0
}
