package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// The cross-kind derivation view. It renders the `derivation` edges from
// config.yml: for each root artifact (e.g. an intent), attach its derived
// artifacts from every other kind, reconstructing the chain through the
// `work_item` frontmatter edge. devtop never drives the lifecycle; the
// view and the user-initiated derive/review actions are generation, and the
// gate (`intents.review == approved`) is enforced here, deterministically.

type PipelineEdge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Transform  string `json:"transform"`
	Gate       string `json:"gate,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Classifier string `json:"classifier,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
}

type PipelineArtifact struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Review string `json:"review"`
	Stale  bool   `json:"stale"`
}

type PipelineItem struct {
	ID        string                        `json:"id"`
	Title     string                        `json:"title"`
	Summary   string                        `json:"summary,omitempty"`
	Review    string                        `json:"review"`
	Stages    map[string][]PipelineArtifact `json:"stages"`
	Ready     bool                          `json:"ready"`
	Stale     bool                          `json:"stale"`
	Uncovered int                           `json:"uncovered"`
}

type PipelineResponse struct {
	Edges []PipelineEdge `json:"edges"`
	Items []PipelineItem `json:"items"`
}

type kindArtifact struct {
	ID    string
	Title string
	Path  string // absolute path
	Rel   string // path relative to the kind root, e.g. architecture/agent-engine.mdx
	Meta  map[string]interface{}
}

func kindExtension(name string) string {
	return kindExtensionFor(engineConfig, name)
}

func kindExtensionFor(cfg EngineConfig, name string) string {
	if k, ok := cfg.ArtifactKinds[name]; ok && k.Extension != "" {
		return k.Extension
	}
	return ".md"
}

func kindRoot(name string) string {
	return kindRootFor(engineConfig, DEVTOP_DIR, name)
}

func kindRootFor(cfg EngineConfig, devTop, name string) string {
	if k, ok := cfg.ArtifactKinds[name]; ok && k.Path != "" {
		return filepath.Join(devTop, k.Path)
	}
	return filepath.Join(devTop, name)
}

// listArtifacts walks a kind dir and returns id/title/meta/absolute path.
func listArtifacts(kindName string) ([]kindArtifact, error) {
	return listArtifactsFor(engineConfig, defaultPaths(), kindName)
}

func listArtifactsFor(cfg EngineConfig, p RepoPaths, kindName string) ([]kindArtifact, error) {
	root := kindRootFor(cfg, p.DevTop, kindName)
	ext := kindExtensionFor(cfg, kindName)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}
	var out []kindArtifact
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ext) {
			return nil
		}
		var meta map[string]interface{}
		if _, perr := parseFrontmatterFile(path, &meta); perr != nil {
			meta = map[string]interface{}{}
		}
		id := artifactIDFromPath(root, path, ext)
		title := ""
		if t, ok := meta["title"].(string); ok {
			title = t
		}
		if title == "" {
			title = id
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, kindArtifact{ID: id, Title: title, Path: path, Rel: rel, Meta: meta})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// resolveArtifactFile finds the file for an artifact id: <id><ext>, or
// <id>/index<ext> when the direct file is absent.
func resolveArtifactFile(kindName, id string) (string, bool) {
	return resolveArtifactFileFor(engineConfig, defaultPaths(), kindName, id)
}

func resolveArtifactFileFor(cfg EngineConfig, p RepoPaths, kindName, id string) (string, bool) {
	root := kindRootFor(cfg, p.DevTop, kindName)
	ext := kindExtensionFor(cfg, kindName)
	id = strings.TrimSuffix(id, ext)
	direct := filepath.Join(root, id+ext)
	if _, err := os.Stat(direct); err == nil {
		return direct, true
	}
	idx := filepath.Join(root, id, "index"+ext)
	if _, err := os.Stat(idx); err == nil {
		return idx, true
	}
	return "", false
}

// resolveArtifactWriteTarget returns the DEVTOP-relative path a write to the
// artifact must target. It prefers an existing file (direct or <id>/index) so
// updates land in place, and falls back to the placement convention: direct
// files for ids with a slash, <id>/index<ext> otherwise. The permission
// mapper (artifactToolPath) and the write_artifact tool share this function,
// so authorization and the actual write always agree.
func resolveArtifactWriteTarget(kindName, id string) (string, bool) {
	return resolveArtifactWriteTargetFor(engineConfig, defaultPaths(), kindName, id)
}
func resolveArtifactWriteTargetFor(cfg EngineConfig, p RepoPaths, kindName, id string) (string, bool) {
	k, ok := cfg.ArtifactKinds[kindName]
	if !ok || !k.AgentWritable || k.View == "board" {
		return "", false
	}
	if id == "" || strings.Contains(id, "..") || strings.Contains(id, "\\") {
		return "", false
	}
	ext := k.Extension
	if ext == "" {
		ext = ".md"
	}
	id = strings.TrimSuffix(id, ext)
	direct := filepath.Join(k.Path, id+ext)
	idx := filepath.Join(k.Path, id, "index"+ext)
	if _, err := os.Stat(filepath.Join(p.DevTop, direct)); err == nil {
		return direct, true
	}
	if _, err := os.Stat(filepath.Join(p.DevTop, idx)); err == nil {
		return idx, true
	}
	if strings.Contains(id, "/") {
		return direct, true
	}
	return idx, true
}

// resolveArtifactReadTarget returns the DEVTOP-relative path of an existing
// artifact of any configured kind. Unlike the write resolver it is open to
// every kind (including `view: board` and non-agent-writable kinds): read
// access is governed by the agent's permissions.read scope, not by the kind's
// write affordances. Shared by read_artifact and its permission mapper.
func resolveArtifactReadTarget(kindName, id string) (string, bool) {
	return resolveArtifactReadTargetFor(engineConfig, defaultPaths(), kindName, id)
}

func resolveArtifactReadTargetFor(cfg EngineConfig, p RepoPaths, kindName, id string) (string, bool) {
	k, ok := cfg.ArtifactKinds[kindName]
	if !ok {
		return "", false
	}
	if id == "" || strings.Contains(id, "..") || strings.Contains(id, "\\") {
		return "", false
	}
	ext := k.Extension
	if ext == "" {
		ext = ".md"
	}
	id = strings.TrimSuffix(id, ext)
	direct := filepath.Join(k.Path, id+ext)
	idx := filepath.Join(k.Path, id, "index"+ext)
	if _, err := os.Stat(filepath.Join(p.DevTop, direct)); err == nil {
		return direct, true
	}
	if _, err := os.Stat(filepath.Join(p.DevTop, idx)); err == nil {
		return idx, true
	}
	return "", false
}

func artifactMetaOf(kindName, id string) (map[string]interface{}, bool, error) {
	return artifactMetaOfFor(engineConfig, defaultPaths(), kindName, id)
}

func artifactMetaOfFor(cfg EngineConfig, p RepoPaths, kindName, id string) (map[string]interface{}, bool, error) {
	path, ok := resolveArtifactFileFor(cfg, p, kindName, id)
	if !ok {
		return nil, false, nil
	}
	var meta map[string]interface{}
	if _, err := parseFrontmatterFile(path, &meta); err != nil {
		meta = map[string]interface{}{}
	}
	return meta, true, nil
}

func pipelineRootKinds() []string {
	return pipelineRootKindsFor(engineConfig)
}

func pipelineRootKindsFor(cfg EngineConfig) []string {
	roots := []string{}
	asTo := map[string]bool{}
	for _, e := range cfg.Derivation {
		asTo[e.To] = true
	}
	for _, e := range cfg.Derivation {
		if !asTo[e.From] {
			roots = append(roots, e.From)
		}
	}
	sort.Strings(roots)
	return roots
}

// staleFromMtime: true when the source changed after the derived artifact.
func staleFromMtime(source, derived string) bool {
	sm, err := os.Stat(source)
	if err != nil {
		return false
	}
	dm, err := os.Stat(derived)
	if err != nil {
		return false
	}
	// A derived-at-equal-mtime (same commit) pair is not stale.
	return sm.ModTime().After(dm.ModTime().Add(time.Second))
}

func buildPipeline() PipelineResponse {
	r := registrySingle()
	if r == nil {
		return PipelineResponse{Edges: []PipelineEdge{}, Items: []PipelineItem{}}
	}
	resp, _ := buildPipelineFor(r)
	return resp
}

func metaReview(meta map[string]interface{}) string {
	if r, ok := meta["review"].(string); ok && r != "" {
		return r
	}
	return "pending"
}

// pipelineWorkItemOf returns the work item id an artifact belongs to: its
// explicit `work_item` frontmatter, or (for a seed) its own id. Artifacts
// without a work_item belong to no chain.
func pipelineWorkItemOf(meta map[string]interface{}) string {
	if wi, ok := meta["work_item"].(string); ok && wi != "" {
		return wi
	}
	return ""
}

// staleArtifact reports whether a derived artifact is out of date relative to
// its recorded source: the `derived_from` reference ("kind/id"), or — when
// absent — its work item seed. mtime is the seam; a content hash recorded at
// derive time supersedes it in the backend.
func staleArtifact(cfg EngineConfig, p RepoPaths, c kindArtifact) bool {
	fromRel, _ := c.Meta["derived_from"].(string)
	if fromRel == "" {
		if wi := pipelineWorkItemOf(c.Meta); wi != "" {
			kindName, id := idParts(wi)
			if kindName != "" {
				if src, ok := resolveArtifactFileFor(cfg, p, kindName, id); ok {
					return staleFromMtime(src, c.Path)
				}
			}
		}
		return false
	}
	kindName, id := idParts(fromRel)
	if kindName == "" {
		return false
	}
	src, ok := resolveArtifactFileFor(cfg, p, kindName, id)
	if !ok {
		return false
	}
	return staleFromMtime(src, c.Path)
}

// idParts splits a "kind/id" reference; malformed references return "".
func idParts(ref string) (string, string) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// buildPipelineFor builds the derivation view for one repo. The second return
// is false when the repo has no configured derivation (no pipeline).
func buildPipelineFor(r *Repo) (PipelineResponse, bool) {
	cfg, err := r.Config()
	if err != nil {
		return PipelineResponse{}, false
	}
	if len(cfg.Derivation) == 0 {
		return PipelineResponse{}, false
	}
	p := r.paths
	edges := make([]PipelineEdge, 0, len(cfg.Derivation))
	for _, e := range cfg.Derivation {
		edges = append(edges, PipelineEdge{From: e.From, To: e.To, Transform: e.Transform, Gate: e.Gate, Agent: e.Agent, Classifier: e.Classifier, Prompt: e.Prompt})
	}

	// Tickets once, keyed by the requirement each anchors, for cover counts.
	tickets, _ := listTicketsP(p)
	ticketed := map[string]bool{}
	for _, t := range tickets {
		if t.Req != "" {
			ticketed[t.Req] = true
		}
	}

	var items []PipelineItem
	for _, root := range pipelineRootKindsFor(cfg) {
		arts, err := listArtifactsFor(cfg, p, root)
		if err != nil {
			continue
		}
		for _, a := range arts {
			it := PipelineItem{
				ID:     a.ID,
				Title:  a.Title,
				Review: metaReview(a.Meta),
				Stages: map[string][]PipelineArtifact{},
			}
			if s, ok := a.Meta["summary"].(string); ok && s != "" {
				it.Summary = s
			}
			// Attach the derived artifacts of every other kind whose
			// work_item names this seed. The work_item edge reconstructs
			// the chain without stored relations.
			for kindName := range cfg.ArtifactKinds {
				if kindName == root {
					continue
				}
				kids, err := listArtifactsFor(cfg, p, kindName)
				if err != nil {
					continue
				}
				for _, c := range kids {
					if pipelineWorkItemOf(c.Meta) != a.ID {
						continue
					}
					stale := staleArtifact(cfg, p, c)
					it.Stages[kindName] = append(it.Stages[kindName], PipelineArtifact{
						ID: c.ID, Title: c.Title, Review: metaReview(c.Meta), Stale: stale,
					})
					if stale {
						it.Stale = true
					}
				}
			}
			// Uncovered: requirements without an anchored ticket.
			for _, c := range it.Stages["requirements"] {
				if !ticketed[c.ID] {
					it.Uncovered++
				}
			}
			// Ready: the seed and every artifact approved, and tickets exist.
			ready := it.Review == "approved" && len(it.Stages["tickets"]) > 0
			if ready {
				for _, stage := range it.Stages {
					for _, c := range stage {
						if c.Review != "approved" {
							ready = false
							break
						}
					}
					if !ready {
						break
					}
				}
			}
			it.Ready = ready
			items = append(items, it)
		}
	}
	if items == nil {
		items = []PipelineItem{}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return PipelineResponse{Edges: edges, Items: items}, true
}

// registrySingle returns the default (first) registered project, or nil when
// none are registered. Used by handlers that run outside request scope.
func registrySingle() *Repo {
	list := registry.List()
	if len(list) == 0 {
		return nil
	}
	return list[0]
}

func handleAPIPipeline(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	resp, present := buildPipelineFor(repo)
	if !present {
		resp = PipelineResponse{Edges: []PipelineEdge{}, Items: []PipelineItem{}}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// evalGate evaluates a declarative gate like "prds.status == approved"
// against the from-artifact of the derive call. Fail closed on any parse or
// match failure.
func evalGate(gate, kindName, id string) bool {
	return evalGateFor(engineConfig, defaultPaths(), gate, kindName, id)
}

func evalGateFor(cfg EngineConfig, p RepoPaths, gate, kindName, id string) bool {
	eq := strings.SplitN(gate, "==", 2)
	if len(eq) != 2 {
		return false
	}
	field := strings.TrimSpace(eq[0])
	want := strings.Trim(strings.TrimSpace(eq[1]), `"'`)
	parts := strings.SplitN(field, ".", 2)
	if len(parts) != 2 || parts[0] != kindName {
		return false
	}
	meta, ok, err := artifactMetaOfFor(cfg, p, kindName, id)
	if err != nil || !ok {
		return false
	}
	got, present := meta[parts[1]]
	if !present {
		return false
	}
	return fmt.Sprintf("%v", got) == want
}

func findDerivationEdge(from, to string) (DerivationEdge, bool) {
	return findDerivationEdgeFor(engineConfig, from, to)
}

func findDerivationEdgeFor(cfg EngineConfig, from, to string) (DerivationEdge, bool) {
	for _, e := range cfg.Derivation {
		if e.From == from && e.To == to {
			return e, true
		}
	}
	return DerivationEdge{}, false
}

// deriveTaskMessage is the user message shaped for the agent running the
// transform. The agent reads the source through its tools and writes the
// target through write tools; the runtime enforces allowlist and scopes.
func deriveTaskMessage(e DerivationEdge, slug string) string {
	return deriveTaskMessageFor(engineConfig, e, slug)
}

// requirementWorkItem returns the work item id a requirement belongs to.
func requirementWorkItem(cfg EngineConfig, p RepoPaths, reqID string) string {
	meta, ok, err := artifactMetaOfFor(cfg, p, "requirements", reqID)
	if err != nil || !ok {
		return ""
	}
	return pipelineWorkItemOf(meta)
}

// requirementsOf returns the sorted requirement ids of one work item.
func requirementsOf(cfg EngineConfig, p RepoPaths, wi string) []string {
	arts, err := listArtifactsFor(cfg, p, "requirements")
	if err != nil {
		return nil
	}
	var ids []string
	for _, a := range arts {
		if pipelineWorkItemOf(a.Meta) == wi {
			ids = append(ids, a.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// uncoveredReqIDs returns the requirements of the source requirement's work
// item that have no ticket anchoring them. All covered returns nil, which the
// caller treats as "nothing to derive" — no model run.
func uncoveredReqIDs(cfg EngineConfig, p RepoPaths, reqID string) []string {
	wi := requirementWorkItem(cfg, p, reqID)
	if wi == "" {
		return nil
	}
	reqs := requirementsOf(cfg, p, wi)
	if len(reqs) == 0 {
		return nil
	}
	tickets, _ := listTicketsP(p)
	anchored := map[string]bool{}
	for _, t := range tickets {
		if t.Req != "" {
			anchored[t.Req] = true
		}
	}
	missing := []string{}
	for _, id := range reqs {
		if !anchored[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

// unapprovedSiblings returns the requirements of the work item (including the
// source) whose review is not approved. Ticket derivation must not run while
// any requirement of the chain is unapproved — the agent would derive from
// content the user has not accepted.
func unapprovedSiblings(cfg EngineConfig, p RepoPaths, reqID string) []string {
	wi := requirementWorkItem(cfg, p, reqID)
	if wi == "" {
		return nil
	}
	var bad []string
	for _, id := range requirementsOf(cfg, p, wi) {
		meta, ok, _ := artifactMetaOfFor(cfg, p, "requirements", id)
		if !ok || metaReview(meta) != "approved" {
			bad = append(bad, id)
		}
	}
	return bad
}

func deriveTaskMessageFor(cfg EngineConfig, e DerivationEdge, slug string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Derive %s artifact(s) from the %s artifact %q using the %q transform.\n\n", e.To, e.From, slug, e.Transform)
	fmt.Fprintf(&b, "Source: kind=%s id=%s\n", e.From, slug)
	if strings.TrimSpace(e.Prompt) != "" {
		b.WriteString(strings.TrimSpace(e.Prompt))
		b.WriteString("\n")
	} else {
		b.WriteString("Read the source artifact with a read tool, then write the derived artifact(s) with write_artifact or create_ticket. Set review: pending in every frontmatter. Call git_commit after every write.\n")
	}
	return b.String()
}

func handleAPIDerive(w http.ResponseWriter, r *http.Request) {
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
		From string `json:"from"`
		To   string `json:"to"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", 400)
		return
	}
	edge, ok := findDerivationEdgeFor(cfg, body.From, body.To)
	if !ok {
		http.Error(w, "derivation edge not declared", 400)
		return
	}
	if body.Slug == "" || strings.Contains(body.Slug, "..") {
		http.Error(w, "invalid slug", 400)
		return
	}

	// Per-target eligibility: a classifier-bound edge refuses to derive until
	// the source prospect for this target is `eligible`.
	if edge.Classifier != "" {
		meta, ok, err := artifactMetaOfFor(cfg, p, body.From, body.Slug)
		if err != nil || !ok {
			http.Error(w, "source artifact not found", 404)
			return
		}
		if verdict, _ := docProspect(meta, edge.To); verdict != "eligible" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "source not eligible for " + edge.To + " derivation (derive_prospects." + edge.To + ")"})
			return
		}
	}

	// Option A: the gate is enforced right here, before any model runs.
	if edge.Gate != "" && !evalGateFor(cfg, p, edge.Gate, body.From, body.Slug) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "gate not satisfied: " + edge.Gate})
		return
	}

	// Delta + whole-chain gate for tickets: the source requirement carries
	// the work item; every sibling requirement must be approved before the
	// agent derives from the chain. All covered means there is nothing to
	// derive — no model call. Partial coverage hands the agent the exact
	// list, turning the model's job into mechanical creation.
	taskMsg := deriveTaskMessageFor(cfg, edge, body.Slug)
	if edge.To == "tickets" {
		if blocked := unapprovedSiblings(cfg, p, body.Slug); len(blocked) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "approve first: " + strings.Join(blocked, ", ")})
			return
		}
		missing := uncoveredReqIDs(cfg, p, body.Slug)
		if missing != nil && len(missing) == 0 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			if flusher, ok := w.(http.Flusher); ok {
				note, _ := json.Marshal(map[string]string{"type": "text", "content": "All requirements already have tickets."})
				fmt.Fprintf(w, "data: %s\n\n", note)
				done, _ := json.Marshal(map[string]string{"type": "done"})
				fmt.Fprintf(w, "data: %s\n\n", done)
				flusher.Flush()
			}
			return
		}
		if missing != nil && len(missing) > 0 {
			taskMsg += fmt.Sprintf("\n\nDelta: create tickets ONLY for these requirements, one ticket per id, with frontmatter req: <id> and source=\"requirements/<id>\", plus the acceptance criteria:\n%s\n", strings.Join(missing, ", "))
		}
	}

	aiCfg := getAPIConfig()
	if !aiCfg.HasKey {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": "AI_API_KEY not configured"})
		return
	}

	rt, err := buildAgentRuntimeFor(repo, edge.Agent)
	if err != nil {
		// No fallback: the deriving agent must be deployed in .devtop/agents/.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "deriving agent not deployed: " + err.Error() + " (run init)",
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

	msgs := []AgentMessage{{Role: "user", Content: taskMsg}}
	outChan := make(chan AgentChunk, 100)
	go func() {
		// Model/agent failures surface as visible stream items inside
		// runAgentWithDepth (before it closes the channel), so a 401 or
		// network error never masquerades as a bare "done".
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
	flusher.Flush()
}

// artifactReviewValues are the review states every artifact carries. The
// reconcile of an artifact is direct (pending|approved|rejected); the mock's
// toggle-to-pending is a client convenience, not an engine rule.
var artifactReviewValues = map[string]bool{"pending": true, "approved": true, "rejected": true}

// handleAPIArtifactReview sets the review state of any config-declared
// artifact. It replaces the removed PRD status endpoint: the target model
// reviews each artifact individually, and the gate names the review field.
// The state change is committed to git, like every write.
func handleAPIArtifactReview(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	cfg, err := repo.Config()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	kindName := r.PathValue("kind")
	kind, ok := cfg.ArtifactKinds[kindName]
	if !ok {
		http.NotFound(w, r)
		return
	}
	ext := kind.Extension
	if ext == "" {
		ext = ".md"
	}
	id := strings.TrimSuffix(r.PathValue("id"), ext)
	if id == "" || strings.Contains(id, "..") || strings.Contains(id, "\\") {
		http.Error(w, "invalid artifact id", 400)
		return
	}
	var body struct {
		Review string `json:"review"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", 400)
		return
	}
	if !artifactReviewValues[body.Review] {
		http.Error(w, "review must be one of pending, approved, rejected", 400)
		return
	}
	path, ok := resolveArtifactFileFor(cfg, repo.paths, kindName, id)
	if !ok {
		http.Error(w, "artifact not found", 404)
		return
	}
	meta, bodyBytes, err := readFrontmatterFile(path)
	if err != nil {
		http.Error(w, "artifact not found", 404)
		return
	}
	meta["review"] = body.Review
	if err := os.WriteFile(path, []byte(composeFrontmatter(meta, string(bodyBytes))), 0644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	gitCommitIn(repo, fmt.Sprintf("%s: set %s review to %s", kindName, id, body.Review))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"id": id, "kind": kindName, "review": body.Review})
}

// readFrontmatterFile parses a frontmatter file into meta and body.
func readFrontmatterFile(path string) (map[string]interface{}, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	var meta map[string]interface{}
	body, err := frontmatterParse(file, &meta)
	if err != nil {
		return nil, nil, err
	}
	if meta == nil {
		meta = map[string]interface{}{}
	}
	return meta, body, nil
}

// composeFrontmatter writes YAML frontmatter + markdown body. yaml.v2 marshals
// nested maps directly, so keys and their ordering from parse survive.
func composeFrontmatter(meta map[string]interface{}, body string) []byte {
	yb, err := yaml.Marshal(meta)
	if err != nil {
		yb = []byte{}
	}
	out := append([]byte("---\n"), yb...)
	out = append(out, []byte("---\n\n")...)
	out = append(out, []byte(body)...)
	return out
}
