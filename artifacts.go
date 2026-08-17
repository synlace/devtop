package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
		return a < b
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

// nextIntentID scans the intents kind dir for the highest INT-<n> already
// used and returns INT-<n+1>, so seeds never collide and re-runs are stable.
func nextIntentID(root, ext string) string {
	files, _ := filepath.Glob(filepath.Join(root, "INT-*"+ext))
	maxID := 0
	for _, f := range files {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSuffix(filepath.Base(f), ext), "INT-%d", &n); err == nil && n > maxID {
			maxID = n
		}
	}
	return fmt.Sprintf("INT-%03d", maxID+1)
}

// handleAPIIntentCreate seeds a new work item. The intent artifact is the
// human-authored seed of a derivation chain (agent_writable=false): it gets
// review: pending, and documentation derives only after the user approves it.
//
//	POST /api/intents  {"title":"...","intent":"..."}
func handleAPIIntentCreate(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	cfg, err := repo.Config()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	kind, ok := cfg.ArtifactKinds["intents"]
	if !ok {
		http.Error(w, "no intents kind configured", 404)
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
	id := nextIntentID(artifactKindRootFor(repo.paths, kind), ext)
	meta := map[string]interface{}{"title": title, "review": "pending"}
	content := composeFrontmatter(meta, intentBody)
	path := filepath.Join(artifactKindRootFor(repo.paths, kind), id+ext)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	gitCommitIn(repo, "intents: create "+id)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      id,
		"title":   title,
		"review":  "pending",
		"content": intentBody,
		"created": true,
	})
}
