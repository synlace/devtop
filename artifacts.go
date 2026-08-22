package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// sameSourceArtifactFor returns the DevTop-relative path and id of the
// artifact of kind whose frontmatter `derived_from` matches the source named
// in the incoming content; the second write targets the same source artifact
// as an existing one, so it must update that file instead of minting a
// sibling (a draft followed by the full artifact must not leave DOC-1 and
// DOC-2 for the same intent).
func sameSourceArtifactFor(cfg EngineConfig, p RepoPaths, kindName, content string) (rel, id string, ok bool) {
	if kindName == "open_questions" {
		// A source legitimately yields several open questions; deduping them
		// would collapse distinct clarifications into one artifact.
		return "", "", false
	}
	var meta map[string]interface{}
	if _, err := parseArtifactFrontmatter(strings.NewReader(content), &meta); err != nil {
		return "", "", false
	}
	source, _ := meta["derived_from"].(string)
	if strings.TrimSpace(source) == "" {
		return "", "", false
	}
	arts, err := listArtifactsFor(cfg, p, kindName)
	if err != nil {
		return "", "", false
	}
	var bestRel, bestID string
	var bestTime time.Time
	for _, a := range arts {
		if s, _ := a.Meta["derived_from"].(string); s != source {
			continue
		}
		// Prefer the newest matching artifact: a stale partial write (the
		// stub) must not shadow the full document written later.
		info, err := os.Stat(a.Path)
		if err != nil {
			continue
		}
		if bestID != "" && !info.ModTime().After(bestTime) {
			continue
		}
		r, err := filepath.Rel(p.DevTop, a.Path)
		if err != nil {
			continue
		}
		bestRel, bestID, bestTime = r, a.ID, info.ModTime()
	}
	if bestID == "" {
		return "", "", false
	}
	return bestRel, bestID, true
}

// Generic artifact API. The engine serves any config-declared kind at
// /api/artifacts/{kind} (list) and /api/artifacts/{kind}/{id...} (detail) by
// walking the kind's configured directory and parsing frontmatter — no
// per-kind handler needed. The classic /api/docs and /api/tickets endpoints
// remain for the views that need their richer shapes (ticket comments, the
// doc-index fallback).
func artifactKindRoot(kind ArtifactKind) string {
	return filepath.Join(DEVTOP_DIR, kind.Path)
}

func artifactKindRootFor(p RepoPaths, kind ArtifactKind) string {
	return filepath.Join(p.DevTop, kind.Path)
}

// artifactIDFromPath derives the id from a file path relative to the kind
// root: "deploy-backfill.mdx" -> "deploy-backfill", "nested/foo.mdx" ->
// "nested/foo", "dir/index.mdx" -> "dir".
func artifactIDFromPath(root, path, ext string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasSuffix(rel, "/index"+ext) {
		return strings.TrimSuffix(rel, "/index"+ext)
	}
	return strings.TrimSuffix(rel, ext)
}

func handleAPIArtifacts(w http.ResponseWriter, r *http.Request) {
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
	root := artifactKindRootFor(p, kind)

	items := []map[string]interface{}{}
	if _, statErr := os.Stat(root); os.IsNotExist(statErr) {
		// Kind dir not created yet — an empty kind is a valid state.
	} else {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ext) {
				return nil
			}
			file, openErr := os.Open(path)
			if openErr != nil {
				return nil
			}
			defer file.Close()
			var meta map[string]interface{}
			if _, parseErr := frontmatterParse(file, &meta); parseErr != nil {
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
			item := map[string]interface{}{"id": id, "title": title}
			for k, v := range meta {
				if k != "title" {
					item[k] = toJSONable(v)
				}
			}
			items = append(items, item)
			return nil
		})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, _ := items[i]["id"].(string)
		b, _ := items[j]["id"].(string)
		return lessArtifactID(a, b)
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

func handleAPIArtifactDetail(w http.ResponseWriter, r *http.Request) {
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
	id := r.PathValue("id")
	id = strings.TrimSuffix(id, ext)
	if id == "" || strings.Contains(id, "..") || strings.Contains(id, "\\") {
		http.NotFound(w, r)
		return
	}
	root := artifactKindRootFor(p, kind)

	filePath := filepath.Join(root, id+ext)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// Support "dir" -> "dir/index.md" like the docs resolver.
		filePath = filepath.Join(root, id, "index"+ext)
	}
	file, err := os.Open(filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	var meta map[string]interface{}
	bodyBytes, parseErr := frontmatterParse(file, &meta)
	if parseErr != nil {
		bodyBytes = []byte{}
		meta = map[string]interface{}{}
	}
	title := ""
	if t, ok := meta["title"].(string); ok {
		title = t
	}
	if title == "" {
		title = id
	}
	resp := map[string]interface{}{
		"id":          id,
		"title":       title,
		"content":     strings.TrimSpace(string(bodyBytes)),
		"frontmatter": toJSONable(meta),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// isSeedKind reports whether a config-declared kind is a work-item seed: it
// must exist and must never appear as the `to` of a derivation edge. Seeds
// are the root of their own chain and are never derived from.
func isSeedKind(cfg EngineConfig, kindName string) bool {
	if _, ok := cfg.ArtifactKinds[kindName]; !ok {
		return false
	}
	for _, e := range cfg.Derivation {
		if e.To == kindName {
			return false
		}
	}
	return true
}

// handleAPIIntentCreate is the legacy route for feature intents. The generic
// seed endpoint serves every seed kind; this keeps POST /api/intents working
// for existing clients and tests.
func handleAPIIntentCreate(w http.ResponseWriter, r *http.Request) {
	handleWorkItemSeedCreate(w, r, "intents")
}

// handleAPISeedCreate seeds a new work item of any seed kind. The seed
// artifact is the human-authored root of a derivation chain
// (agent_writable=false): it gets review: pending, and derivation runs only
// after the user approves it.
//
//	POST /api/seeds/{kind}  {"title":"...","intent":"..."}
func handleAPISeedCreate(w http.ResponseWriter, r *http.Request) {
	handleWorkItemSeedCreate(w, r, r.PathValue("kind"))
}

func handleWorkItemSeedCreate(w http.ResponseWriter, r *http.Request, kindName string) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	cfg, err := repo.Config()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if !isSeedKind(cfg, kindName) {
		http.Error(w, "kind is not a work-item seed: "+kindName, 403)
		return
	}
	kind, ok := cfg.ArtifactKinds[kindName]
	if !ok {
		http.Error(w, "no "+kindName+" kind configured", 404)
		return
	}
	ext := kind.Extension
	if ext == "" {
		ext = ".mdx"
	}
	var body struct {
		Title  string `json:"title"`
		Intent string `json:"intent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", 400)
		return
	}
	title := strings.TrimSpace(body.Title)
	intentBody := strings.TrimSpace(body.Intent)
	if title == "" {
		http.Error(w, "title is required", 400)
		return
	}
	if intentBody == "" {
		intentBody = title
	}
	root := artifactKindRootFor(repo.paths, kind)
	id := mintArtifactID(cfg, repo.paths, kindName)
	meta := map[string]interface{}{"title": title, "review": "pending"}
	content := composeFrontmatter(meta, intentBody)
	path := filepath.Join(root, id+ext)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	gitCommitIn(repo, kindName+": create "+id)
	emitRunEvent("seed.created", kindName, map[string]interface{}{"id": id, "title": title})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      id,
		"kind":    kindName,
		"title":   title,
		"review":  "pending",
		"content": intentBody,
		"created": true,
	})
}
