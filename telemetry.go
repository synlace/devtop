package main

// Tool telemetry: the argument digests a run mirrors and emits for every
// tool call. Digests are first-class so the Run Log and the agent's run_trace
// stay token-cheap — full content stays in the artifact files, readable on
// demand.

// toolArgKeys are the scalar arguments worth recording verbatim. Multi-line
// or bulky arguments (content, description, body) are truncated below.
var toolArgKeys = []string{
	"kind", "id", "path", "commit", "title", "message", "question",
	"status", "priority", "assignee", "source", "req", "work_item",
}

func toolArgsDigest(args map[string]interface{}) map[string]string {
	out := map[string]string{}
	if args == nil {
		return out
	}
	for _, k := range toolArgKeys {
		if v, ok := args[k].(string); ok && v != "" {
			out[k] = head(v, 120)
		}
	}
	if c, ok := args["content"].(string); ok {
		out["content"] = head(c, 200)
	}
	if d, ok := args["description"].(string); ok {
		out["description"] = head(d, 120)
	}
	return out
}
