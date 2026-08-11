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
// config.yml: for each root artifact (e.g. a doc), attach its derived PRD
// and the tickets derived from it. devtop never drives the lifecycle; the
// view and the user-initiated derive/approve actions are generation, and
// the gate (`prds.status == approved`) is enforced here, deterministically.

type PipelineEdge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Transform  string `json:"transform"`
	Gate       string `json:"gate,omitempty"`
	Agent      string `json:"agent,omitempty"`
	Classifier string `json:"classifier,omitempty"`
}

type PipelineTicket struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type PipelinePRD struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Reqs   int    `json:"reqs"`
	Slug   string `json:"slug"`
}

type PipelineItem struct {
	DocID      string           `json:"doc_id"`
	Title      string           `json:"title"`
	Slug       string           `json:"slug"`
	Path       string           `json:"path"`
	Dir        string           `json:"dir"`
	Summary    string           `json:"summary,omitempty"`
	Prospect   string           `json:"prospect,omitempty"`
	ProspectBy string           `json:"prospect_by,omitempty"`
	PRD        *PipelinePRD     `json:"prd,omitempty"`
	Tickets    []PipelineTicket `json:"tickets"`
	Stale      bool             `json:"stale"`
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
	resp, _ := buildPipelineFor(registrySingle())
	return resp
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
		edges = append(edges, PipelineEdge{From: e.From, To: e.To, Transform: e.Transform, Gate: e.Gate, Agent: e.Agent, Classifier: e.Classifier})
	}

	var items []PipelineItem
	tickets, _ := listTicketsP(p)
	ticketBySource := map[string][]Ticket{}
	for _, t := range tickets {
		if t.Source != "" {
			ticketBySource[t.Source] = append(ticketBySource[t.Source], t)
		}
	}

	for _, root := range pipelineRootKindsFor(cfg) {
		arts, err := listArtifactsFor(cfg, p, root)
		if err != nil {
			continue
		}
		// The view prospect is the verdict for the first outgoing edge.
		target := ""
		for _, e := range cfg.Derivation {
			if e.From == root {
				target = e.To
				break
			}
		}
		for _, a := range arts {
			item := PipelineItem{
				DocID:      a.ID,
				Title:      a.Title,
				Slug:       a.ID,
				Path:       a.Rel,
				Tickets:    []PipelineTicket{},
				Prospect:   "",
				ProspectBy: "",
			}
			if target != "" {
				item.Prospect, item.ProspectBy = docProspect(a.Meta, target)
			}
			item.Dir = ""
			if i := strings.IndexByte(a.Rel, '/'); i >= 0 {
				item.Dir = a.Rel[:i]
			}
			if s, ok := a.Meta["summary"].(string); ok && s != "" {
				item.Summary = s
			} else if s, ok := a.Meta["description"].(string); ok && s != "" {
				item.Summary = s
			}
			// First outgoing edge maps this artifact to a derived artifact.
			for _, e := range cfg.Derivation {
				if e.From != root {
					continue
				}
				if dpath, ok := resolveArtifactFileFor(cfg, p, e.To, a.ID); ok {
					meta, _, _ := artifactMetaOfFor(cfg, p, e.To, a.ID)
					prd := &PipelinePRD{ID: a.ID, Slug: a.ID}
					if t, ok := meta["title"].(string); ok {
						prd.Title = t
					}
					if s, ok := meta["status"].(string); ok {
						prd.Status = s
					}
					if reqs, ok := meta["requirements"].([]interface{}); ok {
						prd.Reqs = len(reqs)
					}
					item.PRD = prd
					if e.To == "prds" || strings.Contains(e.Transform, "breakdown") {
						item.Stale = staleFromMtime(a.Path, dpath)
					}
				}
				// Tickets derive from the PRD, keyed by its source slug.
				for _, t := range ticketBySource["prds/"+a.ID] {
					item.Tickets = append(item.Tickets, PipelineTicket{ID: t.ID, Title: t.Title, Status: t.Status})
				}
				sort.Slice(item.Tickets, func(i, j int) bool { return item.Tickets[i].ID < item.Tickets[j].ID })
			}
			items = append(items, item)
		}
	}
	if items == nil {
		items = []PipelineItem{}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DocID < items[j].DocID })
	return PipelineResponse{Edges: edges, Items: items}, true
}

// registrySingle returns the legacy default repo (single-repo mode) or the
// first registered repo, for handlers that run outside request scope.
func registrySingle() *Repo {
	repo, _ := registry.Resolve("")
	return repo
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

func deriveTaskMessageFor(cfg EngineConfig, e DerivationEdge, slug string) string {
	toExt := kindExtensionFor(cfg, e.To)
	toRoot := cfg.ArtifactKinds[e.To].Path
	var b strings.Builder
	fmt.Fprintf(&b, "Derive a %s artifact from the %s artifact %q using the %q transform.\n\n", e.To, e.From, slug, e.Transform)
	fmt.Fprintf(&b, "Target kind: %s\n", e.To)
	fmt.Fprintf(&b, "Target id: %s\n", slug)
	fmt.Fprintf(&b, "Target file: .devtop/%s/%s/%sindex%s (or .devtop/%s/%s%s)\n\n", toRoot, slug, "", toExt, toRoot, slug, toExt)
	fmt.Fprintf(&b, "Steps:\n1. Read the source artifact %q with a read tool.\n", slug)
	if e.To == "tickets" {
		fmt.Fprintf(&b, "2. Create one ticket per requirement from the PRD with create_ticket, passing source=\"prds/%s\".\n", slug)
	} else {
		fmt.Fprintf(&b, "2. Write the draft with write_artifact: kind=%q, id=%q. The content has YAML frontmatter, including title and status: draft.\n", e.To, slug)
	}
	fmt.Fprintf(&b, "3. Call git_commit after every write.\n")
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

	msgs := []AgentMessage{{Role: "user", Content: deriveTaskMessageFor(cfg, edge, body.Slug)}}
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
	flusher.Flush()
}

// prdStatusTransitions is the PRD state machine. Allowed: one step at a time,
// plus returning to draft from anywhere except archived (not modeled).
var prdStatusTransitions = map[string]map[string]bool{
	"draft":     {"reviewing": true},
	"reviewing": {"approved": true, "draft": true},
	"approved":  {"draft": true},
}

func handleAPIPRDStatus(w http.ResponseWriter, r *http.Request) {
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
	slug := strings.TrimSuffix(r.PathValue("slug"), ".mdx")
	if slug == "" || strings.Contains(slug, "..") {
		http.Error(w, "invalid slug", 400)
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", 400)
		return
	}
	path, ok := resolveArtifactFileFor(cfg, p, "prds", slug)
	if !ok {
		http.Error(w, "PRD not found", 404)
		return
	}
	meta, bodyBytes, err := readFrontmatterFile(path)
	if err != nil {
		http.Error(w, "PRD not found", 404)
		return
	}
	prev, _ := meta["status"].(string)
	if prev == "" {
		prev = "draft"
	}
	if prev != body.Status && !prdStatusTransitions[prev][body.Status] {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("invalid transition %s -> %s", prev, body.Status)})
		return
	}
	meta["status"] = body.Status
	out := composeFrontmatter(meta, string(bodyBytes))
	if err := os.WriteFile(path, []byte(out), 0644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	gitCommitIn(repo, fmt.Sprintf("prds: set %s status to %s", slug, body.Status))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"id": slug, "status": body.Status})
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
