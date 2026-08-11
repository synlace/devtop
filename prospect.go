package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Per-target eligibility. A source artifact (e.g. a doc) declares in its
// frontmatter, for each derivation target, whether it qualifies:
//
//	derive_prospects:
//	  prds: eligible | not-eligible
//	prospect_by: model | user
//
// Absent verdict means unassessed. `model` is the classify agent's
// suggestion; `user` is the override and is the sealed decision. Reads and
// writes both go through the artifact frontmatter, so the pipeline view and
// the derive handler share one source of truth.

const (
	prospectField   = "derive_prospects"
	prospectByField = "prospect_by"
)

// nestedStringMap normalizes either yaml.v2 nested map (interface keys) or a
// string-keyed map into a string-keyed map.
func nestedStringMap(v interface{}) map[string]interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = val
		}
		return out
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[fmt.Sprintf("%v", k)] = val
		}
		return out
	}
	return nil
}

// docProspect reads the verdict and provenance for one derivation target.
// verdict is "" when unassessed or the target is undeclared.
func docProspect(meta map[string]interface{}, target string) (verdict, by string) {
	by, _ = meta[prospectByField].(string)
	m := nestedStringMap(meta[prospectField])
	if m != nil {
		if v, ok := m[target].(string); ok && (v == "eligible" || v == "not-eligible") {
			return v, by
		}
	}
	return "", by
}

// firstEdgeFrom returns the first derivation edge leaving a kind; it is the
// edge whose target the prospect belongs to.
func firstEdgeFrom(kind string) (DerivationEdge, bool) {
	return firstEdgeFromFor(engineConfig, kind)
}

func firstEdgeFromFor(cfg EngineConfig, kind string) (DerivationEdge, bool) {
	for _, e := range cfg.Derivation {
		if e.From == kind {
			return e, true
		}
	}
	return DerivationEdge{}, false
}

func validArtifactKind(kind string) bool {
	_, ok := engineConfig.ArtifactKinds[kind]
	return ok
}

func validArtifactKindFor(cfg EngineConfig, kind string) bool {
	_, ok := cfg.ArtifactKinds[kind]
	return ok
}

// writeProspectVerdict sets the user decision for an edge target and seals it
// with prospect_by: user. It preserves any other target verdicts and the
// artifact body.
func writeProspectVerdict(kind, slug, verdict string) error {
	return writeProspectVerdictFor(engineConfig, defaultPaths(), kind, slug, verdict)
}

func writeProspectVerdictFor(cfg EngineConfig, p RepoPaths, kind, slug, verdict string) error {
	path, ok := resolveArtifactFileFor(cfg, p, kind, slug)
	if !ok {
		return fmt.Errorf("artifact %s/%s not found", kind, slug)
	}
	meta, bodyBytes, err := readFrontmatterFile(path)
	if err != nil {
		return fmt.Errorf("artifact %s/%s not found", kind, slug)
	}
	edge, okEdge := firstEdgeFromFor(cfg, kind)
	if !okEdge {
		return fmt.Errorf("no derivation edge from kind %s", kind)
	}
	prospects := nestedStringMap(meta[prospectField])
	if prospects == nil {
		prospects = map[string]interface{}{}
	}
	prospects[edge.To] = verdict
	meta[prospectField] = prospects
	meta[prospectByField] = "user"
	out := composeFrontmatter(meta, string(bodyBytes))
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		return err
	}
	gitCommitIn(forRepoOf(p), fmt.Sprintf("%s: set %s prospect for %s to %s (user)", kind, slug, edge.To, verdict))
	return nil
}

// handleAPIProspect posts a user verdict for one source artifact:
//
//	POST /api/pipeline/prospect  {"kind":"docs","slug":"...","verdict":"eligible"}
func handleAPIProspect(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	cfg, err := repo.Config()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var body struct {
		Kind    string `json:"kind"`
		Slug    string `json:"slug"`
		Verdict string `json:"verdict"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", 400)
		return
	}
	if !validArtifactKindFor(cfg, body.Kind) {
		http.Error(w, "unknown artifact kind", 400)
		return
	}
	if body.Slug == "" || strings.Contains(body.Slug, "..") {
		http.Error(w, "invalid slug", 400)
		return
	}
	if body.Verdict != "eligible" && body.Verdict != "not-eligible" {
		http.Error(w, "verdict must be eligible or not-eligible", 400)
		return
	}
	if _, ok := firstEdgeFromFor(cfg, body.Kind); !ok {
		http.Error(w, "no derivation edge from kind "+body.Kind, 400)
		return
	}
	if err := writeProspectVerdictFor(cfg, repo.paths, body.Kind, body.Slug, body.Verdict); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), 404)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	meta, _, _ := artifactMetaOfFor(cfg, repo.paths, body.Kind, body.Slug)
	edge, _ := firstEdgeFromFor(cfg, body.Kind)
	verdict, by := docProspect(meta, edge.To)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":          body.Slug,
		"prospect":    verdict,
		"prospect_by": by,
	})
}

// classifyTaskMessage is the user message for the classifier agent. The agent
// reads the source through its tools and writes the suggestion through
// write_artifact; the runtime enforces allowlist and scopes. The message pins
// the target file explicitly: a classify run that writes elsewhere is damage,
// not assistance.
func classifyTaskMessage(edge DerivationEdge, slug string) string {
	return classifyTaskMessageFor(engineConfig, defaultPaths(), edge, slug)
}

func classifyTaskMessageFor(cfg EngineConfig, p RepoPaths, edge DerivationEdge, slug string) string {
	rel, ok := resolveArtifactWriteTargetFor(cfg, p, edge.From, slug)
	if !ok {
		rel = edge.From + "/" + slug
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Assess whether the %s artifact %q qualifies as material for deriving a %s artifact.\n\n", edge.From, slug, edge.To)
	fmt.Fprintf(&b, "Read only this file: .devtop/%s\n\n", rel)
	fmt.Fprintf(&b, "Write the verdict back to the SAME file with write_artifact.\n")
	fmt.Fprintf(&b, "The ONLY file you may write is .devtop/%s. Writing any other file is an error.\n", rel)
	fmt.Fprintf(&b, "Call write_artifact with kind %q, id %q, and the COMPLETE file content.\n", edge.From, strings.TrimSuffix(slug, kindExtensionFor(cfg, edge.From)))
	fmt.Fprintf(&b, "Change exactly two things in the YAML frontmatter:\n")
	fmt.Fprintf(&b, "  set %s:\n    %s: eligible | not-eligible\n", prospectField, edge.To)
	fmt.Fprintf(&b, "  add the top-level field %s: model\n", prospectByField)
	fmt.Fprintf(&b, "Keep every other line of the file verbatim, including the body.\n")
	fmt.Fprintf(&b, "Eligible means the artifact states the problem, the scope, and the intended outcome of a product change.\n")
	fmt.Fprintf(&b, "Never change an artifact whose frontmatter already has %s: user.\n", prospectByField)
	fmt.Fprintf(&b, "Call git_commit after the write.\n")
	return b.String()
}

// classifyResult reads the source artifact after a classify run. written is
// true only when the target file carries a verdict for the edge target with a
// top-level prospect_by == "model" — anything else surfaces as a failed run.
func classifyResult(kind, slug string, edge DerivationEdge) (verdict, by string, written bool) {
	meta, ok, _ := artifactMetaOf(kind, slug)
	if !ok {
		return "", "", false
	}
	v, b := docProspect(meta, edge.To)
	if v == "" || b != "model" {
		return v, b, false
	}
	return v, b, true
}

func classifyResultFor(cfg EngineConfig, p RepoPaths, kind, slug string, edge DerivationEdge) (verdict, by string, written bool) {
	meta, ok, _ := artifactMetaOfFor(cfg, p, kind, slug)
	if !ok {
		return "", "", false
	}
	v, b := docProspect(meta, edge.To)
	if v == "" || b != "model" {
		return v, b, false
	}
	return v, b, true
}

// handleAPIProspectClassify runs the bound classifier agent and streams the
// model activity:
//
//	POST /api/pipeline/prospect/classify  {"kind":"docs","slug":"..."}
func handleAPIProspectClassify(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	cfg, err := repo.Config()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	p := repo.paths
	var body struct {
		Kind string `json:"kind"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", 400)
		return
	}
	if !validArtifactKindFor(cfg, body.Kind) {
		http.Error(w, "unknown artifact kind", 400)
		return
	}
	if body.Slug == "" || strings.Contains(body.Slug, "..") {
		http.Error(w, "invalid slug", 400)
		return
	}
	edge, ok := firstEdgeFromFor(cfg, body.Kind)
	if !ok {
		http.Error(w, "no derivation edge from kind "+body.Kind, 400)
		return
	}
	if edge.Classifier == "" {
		http.Error(w, "no classifier agent bound to the "+body.Kind+" edge", 404)
		return
	}
	meta, okMeta, errMeta := artifactMetaOfFor(cfg, p, body.Kind, body.Slug)
	if errMeta != nil || !okMeta {
		http.Error(w, "artifact not found", 404)
		return
	}
	if _, by := docProspect(meta, edge.To); by == "user" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "user verdict exists; the classifier does not overwrite it"})
		return
	}

	aiCfg := getAPIConfig()
	if !aiCfg.HasKey {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "AI_API_KEY not configured"})
		return
	}

	rt, err := buildAgentRuntimeFor(repo, edge.Classifier)
	if err != nil {
		// No fallback: the classifier must be deployed in .devtop/agents/.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "classifier agent not deployed: " + err.Error() + " (run init)",
		})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", 500)
		return
	}

	msgs := []AgentMessage{{Role: "user", Content: classifyTaskMessageFor(cfg, p, edge, body.Slug)}}
	outChan := make(chan AgentChunk, 100)
	go func() {
		_ = runAgentInRepo(context.Background(), repo, msgs, aiCfg.APIKey, aiCfg.BaseURL, aiCfg.Model, rt, outChan)
	}()

	for chunk := range outChan {
		if chunk.Type == "text" {
			payload, _ := json.Marshal(map[string]string{"type": "text", "content": chunk.Content})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		} else if chunk.Type == "tool_call" {
			prev := chunk.Result
			if len(prev) > 200 {
				prev = prev[:200]
			}
			payload, _ := json.Marshal(map[string]string{"type": "tool_call", "name": chunk.Name, "result": prev})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
	done, _ := json.Marshal(map[string]string{"type": "done"})
	fmt.Fprintf(w, "data: %s\n\n", done)
	// Report the resulting verdict so the UI shows success, a malformed
	// write, or silence instead of pretending the doc was assessed.
	v, by, written := classifyResultFor(cfg, p, body.Kind, body.Slug, edge)
	payload := map[string]interface{}{"type": "prospect", "prospect": v, "prospect_by": by, "written": written}
	if !written {
		if v == "" {
			payload["note"] = fmt.Sprintf("no %s verdict was written to %s", edge.To, body.Slug)
		} else {
			payload["note"] = fmt.Sprintf("verdict on %s is missing the top-level %s field", body.Slug, prospectByField)
		}
	}
	ev, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", ev)
	flusher.Flush()
}

// assessLocks serialize automatic assessments per repo root, so concurrent
// classifier runs in one repo never collide on the same doc or git index.
var (
	assessLocksMu sync.Mutex
	assessLocks   = map[string]*sync.Mutex{}
)

func assessLockFor(root string) *sync.Mutex {
	assessLocksMu.Lock()
	defer assessLocksMu.Unlock()
	m, ok := assessLocks[root]
	if !ok {
		m = &sync.Mutex{}
		assessLocks[root] = m
	}
	return m
}

// assessmentTarget returns the (kind, slug) a write tool produced, or ok=false
// when the write has no classifier-bound derivation edge. It feeds the
// automatic assessment that runs after the chat agent writes a doc.
func assessmentTarget(repo *Repo, name string, args map[string]interface{}) (kind, slug string, ok bool) {
	if repo == nil {
		return "", "", false
	}
	cfg, err := repo.Config()
	if err != nil {
		return "", "", false
	}
	var k, id string
	switch name {
	case "write_artifact":
		k, _ = args["kind"].(string)
		id, _ = args["id"].(string)
	case "write_doc":
		k = "docs"
		rel, relOK := docToolPath(args)
		if !relOK {
			return "", "", false
		}
		ak := cfg.ArtifactKinds[k]
		id = strings.TrimPrefix(strings.TrimSuffix(rel, ak.Extension), ak.Path+"/")
		id = strings.TrimSuffix(id, "/index")
	default:
		return "", "", false
	}
	kindDef, hasKind := cfg.ArtifactKinds[k]
	if !hasKind || id == "" {
		return "", "", false
	}
	id = strings.TrimSuffix(id, kindDef.Extension)
	edge, ok2 := firstEdgeFromFor(cfg, k)
	if !ok2 || edge.Classifier == "" {
		return "", "", false
	}
	return k, id, true
}

// assessArtifact runs the classifier agent for one source artifact in the
// background, after the chat agent wrote or updated it. It mirrors the manual
// "Suggest eligibility" flow without streaming: the classifier writes the
// verdict to the same file and commits it. Every failure is dropped —
// assessment must never break the originating tool call or the chat.
func assessArtifact(repo *Repo, kind, slug string) {
	lock := assessLockFor(repo.Root)
	lock.Lock()
	defer lock.Unlock()

	cfg, err := repo.Config()
	if err != nil {
		return
	}
	edge, ok := firstEdgeFromFor(cfg, kind)
	if !ok || edge.Classifier == "" {
		return
	}
	p := repo.paths
	meta, metaOK, _ := artifactMetaOfFor(cfg, p, kind, slug)
	if !metaOK {
		return
	}
	if _, by := docProspect(meta, edge.To); by == "user" {
		return
	}
	aiCfg := getAPIConfig()
	if !aiCfg.HasKey {
		return
	}
	rt, err := buildAgentRuntimeFor(repo, edge.Classifier)
	if err != nil {
		return
	}
	msgs := []AgentMessage{{Role: "user", Content: classifyTaskMessageFor(cfg, p, edge, slug)}}
	out := make(chan AgentChunk, 100)
	_ = runAgentInRepo(context.Background(), repo, msgs, aiCfg.APIKey, aiCfg.BaseURL, aiCfg.Model, rt, out)
	for range out {
	}
}
