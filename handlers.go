package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	APIKey      string `json:"api_key"`
	BaseURL     string `json:"base_url"`
	Model       string `json:"model"`
	Provider    string `json:"provider"`
	DevtopDir   string `json:"devtop_dir"`
	HasKey      bool   `json:"has_key"`
	AgentPrompt string `json:"agent_prompt"`
	MCPServers  int    `json:"mcp_servers"`
}

func getAPIConfig() Config {
	baseURL := os.Getenv("AI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	model := os.Getenv("AI_MODEL")
	if model == "" {
		model = "deepseek/deepseek-v4-flash-0731"
	}
	apiKey := os.Getenv("AI_API_KEY")

	provider := "other"
	if strings.Contains(baseURL, "openrouter") {
		provider = "openrouter"
	} else if strings.Contains(baseURL, "localhost") || strings.Contains(baseURL, "127.0.0.1") {
		provider = "lmstudio"
	}

	mcpCfgs := parseMCPServers()

	return Config{
		APIKey:      apiKey,
		BaseURL:     baseURL,
		Model:       model,
		Provider:    provider,
		DevtopDir:   DEVTOP_DIR,
		HasKey:      apiKey != "" && apiKey != "not-needed",
		AgentPrompt: resolveActivePrompt(),
		MCPServers:  len(mcpCfgs),
	}
}

// SPA Routes — the Go server serves the built React app from frontend/dist.
// Any non-API path falls back to index.html so React's hash routing can handle
// it. If the frontend isn't built, a clear message is returned instead.
func handleSPA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	indexPath := filepath.Join(STATIC_DIR, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, "<h1>devtop frontend not built</h1><p>Run <code>just devtop build</code>, or use <code>just devtop dev</code> for the Vite HMR dev server.</p>")
		return
	}

	rel := strings.TrimPrefix(r.URL.Path, "/")
	if rel != "" && !strings.Contains(rel, "..") {
		if fi, err := os.Stat(filepath.Join(STATIC_DIR, rel)); err == nil && !fi.IsDir() {
			http.ServeFile(w, r, filepath.Join(STATIC_DIR, rel))
			return
		}
	}
	http.ServeFile(w, r, indexPath)
}

// CopilotKit runtime proxy. The browser only ever talks to the Go server on one
// port; /api/copilotkit/* is forwarded to the Node runtime (started by the dev
// recipe or the Docker entrypoint). No path rewrite: the runtime serves at the
// same base path.
func handleCopilotKitProxy(w http.ResponseWriter, r *http.Request) {
	rtURL := os.Getenv("COPILOTKIT_RUNTIME_URL")
	if rtURL == "" {
		rtURL = "http://127.0.0.1:4000"
	}
	target, err := url.Parse(rtURL)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ServeHTTP(w, r)
}

// API Routes
func handleAPIDocs(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	docs, err := listDocsP(repo.paths)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(docs)
}

func handleAPIDocPage(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	p := repo.paths
	slug := r.PathValue("slug")
	slug = strings.TrimSuffix(slug, ".mdx")

	title, htmlContent, err := getDocP(p, slug)
	if err != nil {
		if slug == "index" {
			docs, _ := listDocsP(p)
			if len(docs) > 0 {
				title, htmlContent, err = getDocP(p, docs[0].Slug)
			} else {
				title, htmlContent, err = getWelcomeDoc()
			}
		}
		if err != nil {
			http.Error(w, "Document not found", 404)
			return
		}
	}

	resp := map[string]string{
		"title":   title,
		"content": htmlContent,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleAPITickets(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	tickets, err := listTicketsP(repo.paths)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tickets)
}

func handleAPITicketDetail(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	t, err := getTicketP(repo.paths, id)
	if err != nil {
		http.Error(w, "Ticket not found", 404)
		return
	}

	t.Comments = extractComments(t.RawDescription)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

func handleAPIThreads(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	p := repo.paths
	context := r.URL.Query().Get("context")
	threads, err := listThreadsP(p, context)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var resp []map[string]interface{}
	for _, t := range threads {
		messages, _ := t["messages"].([]interface{})
		preview := ""
		if len(messages) > 0 {
			if last, ok := messages[len(messages)-1].(map[string]interface{}); ok {
				content, _ := last["content"].(string)
				if len(content) > 80 {
					preview = content[:80]
				} else {
					preview = content
				}
			}
		}

		id, _ := t["id"].(string)
		ctx, _ := t["context"].(string)
		title, _ := t["title"].(string)
		createdAt, _ := t["created_at"].(string)
		updatedAt, _ := t["updated_at"].(string)

		resp = append(resp, map[string]interface{}{
			"id":            id,
			"context":       ctx,
			"title":         title,
			"created_at":    createdAt,
			"updated_at":    updatedAt,
			"message_count": len(messages),
			"preview":       preview,
		})
	}

	if resp == nil {
		resp = []map[string]interface{}{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleAPICreateThread(w http.ResponseWriter, r *http.Request) {
	// A zero-repo instance has no thread store: the chat is gated until a
	// repo is added, so refuse before writing anything to the workspace.
	if zeroRepoInstance() {
		writeJSONError(w, http.StatusConflict, "no repo selected")
		return
	}
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	p := repo.paths
	var payload map[string]string
	_ = json.NewDecoder(r.Body).Decode(&payload)

	randBytes := make([]byte, 6)
	_, _ = rand.Read(randBytes)
	tid := "thread-" + hex.EncodeToString(randBytes)

	now := time.Now().UTC().Format(time.RFC3339)
	context := payload["context"]
	if context == "" {
		context = "global"
	}
	title := payload["title"]
	if title == "" {
		title = "New conversation"
	}

	initialMsg := map[string]interface{}{
		"role":       "agent",
		"content":    "Ready to help. What would you like to work on?",
		"timestamp":  now,
		"tool_calls": []interface{}{},
	}

	messages := []interface{}{initialMsg}

	threadMap := map[string]interface{}{
		"id":         tid,
		"context":    context,
		"title":      title,
		"created_at": now,
		"updated_at": now,
		"messages":   messages,
	}

	if err := writeThreadToFileSystemP(p, tid, threadMap); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(threadMap)
}

func handleAPIGetThread(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	t, err := getThreadP(repo.paths, id)
	if err != nil {
		http.Error(w, "Thread not found", 404)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

func handleAPIDeleteThread(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Thread ID is required", 400)
		return
	}
	if err := deleteThreadFileP(repo.paths, id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIChat(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	p := repo.paths
	threadID := r.PathValue("thread_id")
	threadMap, err := getThreadP(p, threadID)
	if err != nil {
		http.Error(w, "Thread not found", 404)
		return
	}

	var payload map[string]string
	_ = json.NewDecoder(r.Body).Decode(&payload)
	userMsg := strings.TrimSpace(payload["message"])
	if userMsg == "" {
		http.Error(w, "Message is required", 400)
		return
	}

	cfg := getAPIConfig()
	if cfg.APIKey == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": "AI_API_KEY not configured"})
		return
	}

	msgsSlice, _ := threadMap["messages"].([]interface{})
	now := time.Now().UTC().Format(time.RFC3339)

	newHumanMsg := map[string]interface{}{
		"role":       "human",
		"content":    userMsg,
		"timestamp":  now,
		"tool_calls": []interface{}{},
	}
	msgsSlice = append(msgsSlice, newHumanMsg)
	threadMap["messages"] = msgsSlice

	threadTitle, _ := threadMap["title"].(string)
	if threadTitle == "Home discussion" {
		title, err := generateThreadTitle(r.Context(), userMsg, cfg.APIKey, cfg.BaseURL, cfg.Model)
		if err == nil && title != "" {
			threadMap["title"] = title
		}
	}

	_ = writeThreadToFileSystemP(p, threadID, threadMap)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", 500)
		return
	}

	var agentMsgs []AgentMessage
	for _, m := range msgsSlice {
		mMap, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		role, _ := mMap["role"].(string)
		if role == "human" {
			role = "user"
		} else if role == "agent" {
			role = "assistant"
		}
		content, _ := mMap["content"].(string)
		agentMsgs = append(agentMsgs, AgentMessage{Role: role, Content: content})
	}

	outChan := make(chan AgentChunk, 100)
	rt := activeRuntimeFor(repo)
	if rt == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "no agent configured: initialize the repo to scaffold .devtop/agents",
		})
		return
	}
	go func() {
		_ = runAgentInRepo(context.Background(), repo, agentMsgs, cfg.APIKey, cfg.BaseURL, cfg.Model, rt, outChan)
	}()

	agentResponse := ""
	var toolCallsMade []map[string]interface{}

	for chunk := range outChan {
		if chunk.Type == "text" {
			agentResponse += chunk.Content
			respBytes, _ := json.Marshal(map[string]string{
				"type":    "text",
				"content": chunk.Content,
			})
			fmt.Fprintf(w, "data: %s\n\n", string(respBytes))
			flusher.Flush()
		} else if chunk.Type == "tool_call" {
			toolCallsMade = append(toolCallsMade, map[string]interface{}{
				"name":      chunk.Name,
				"arguments": chunk.Arguments,
				"result":    chunk.Result,
			})

			if strings.HasPrefix(chunk.Result, "__ASK_USER__:") {
				question := strings.TrimPrefix(chunk.Result, "__ASK_USER__:")
				respBytes, _ := json.Marshal(map[string]string{
					"type":     "question",
					"name":     chunk.Name,
					"question": question,
					"result":   "",
				})
				fmt.Fprintf(w, "data: %s\n\n", string(respBytes))
				flusher.Flush()
			} else {
				prev := chunk.Result
				if len(prev) > 200 {
					prev = prev[:200]
				}
				respBytes, _ := json.Marshal(map[string]string{
					"type":      "tool_call",
					"name":      chunk.Name,
					"arguments": chunk.Arguments,
					"result":    prev,
				})
				fmt.Fprintf(w, "data: %s\n\n", string(respBytes))
				flusher.Flush()
			}
		}
	}

	if agentResponse != "" {
		now = time.Now().UTC().Format(time.RFC3339)
		newAgentMsg := map[string]interface{}{
			"role":       "agent",
			"content":    agentResponse,
			"timestamp":  now,
			"tool_calls": toolCallsMade,
		}
		msgsSlice = append(msgsSlice, newAgentMsg)
		threadMap["messages"] = msgsSlice
		threadMap["updated_at"] = now

		_ = writeThreadToFileSystemP(p, threadID, threadMap)
	}

	doneBytes, _ := json.Marshal(map[string]string{"type": "done"})
	fmt.Fprintf(w, "data: %s\n\n", string(doneBytes))
	flusher.Flush()
}

func handleAPIModels(w http.ResponseWriter, r *http.Request) {
	cfg := getAPIConfig()
	// Cache under the active repo's data dir. A zero-repo instance keeps the
	// cache in memory: it skips the fetch entirely and writes nothing to the
	// workspace until a repo is added.
	models := []ModelInfo{}
	if !zeroRepoInstance() {
		var dataDir string
		if repo, ok := repoFromRequest(w, r); ok {
			dataDir = repo.paths.Data
		}
		models, _ = fetchModels(cfg.BaseURL, cfg.APIKey, dataDir)
	}
	resp := map[string]interface{}{
		"models": models,
		"config": cfg,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	cfg := getAPIConfig()
	// The chat is always the active repo's default agent: expose its prompt
	// (or an explicit empty, never a fallback) when a repo is selected.
	if repo, ok := repoFromRequest(w, r); ok {
		if rt := activeRuntimeFor(repo); rt != nil {
			cfg.AgentPrompt = rt.prompt
		} else {
			cfg.AgentPrompt = ""
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

func extractComments(content string) []Comment {
	var comments []Comment
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "**") && strings.Contains(line, "—") {
			parts := strings.SplitN(line, "—", 2)
			if len(parts) == 2 {
				dateAuthor := strings.Trim(parts[0], " *")
				text := strings.TrimSpace(parts[1])
				author := ""
				if strings.Contains(text, ":") {
					subparts := strings.SplitN(text, ":", 2)
					potentialAuthor := strings.TrimSpace(subparts[0])
					if !strings.Contains(potentialAuthor, " ") {
						author = potentialAuthor
						text = strings.TrimSpace(subparts[1])
					}
				}
				comments = append(comments, Comment{
					Date:   dateAuthor,
					Author: author,
					Text:   text,
				})
			}
		}
	}
	return comments
}

// Revision history API — the git history of a doc (or ticket) file, plus
// content-at-commit and unified diffs. git-diff-view consumes `diff` verbatim.
//
//	GET /api/revisions/docs/{slug...}            -> list of revisions
//	GET /api/revisions/docs/{slug...}?at=<sha>   -> {title, content, deleted}
//	GET /api/revisions/docs/{slug...}?a=<a>&b=<b> -> {diff}
//
// Same shapes under /api/revisions/tickets/{id}.
func handleAPIDocRevisions(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	slug := r.PathValue("slug")
	path, err := docPathForSlugP(repo.paths, slug)
	if err != nil {
		http.Error(w, "Document not found", 404)
		return
	}
	serveRevisionRequest(w, r, repo, path)
}

func handleAPITicketRevisions(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	path := filepath.Join(repo.paths.Tickets, id+".md")
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "Ticket not found", 404)
		return
	}
	serveRevisionRequest(w, r, repo, path)
}

func serveRevisionRequest(w http.ResponseWriter, r *http.Request, repo *Repo, path string) {
	at := r.URL.Query().Get("at")
	from := r.URL.Query().Get("a")
	to := r.URL.Query().Get("b")

	root, err := findRepoRootFrom(repo.paths.DevTop)
	if err != nil {
		// In a non-git workspace every history op fails at the root; surface once.
		http.Error(w, "History unavailable: not a git repository", 409)
		return
	}
	rel, err := gitRelPathFrom(root, path)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch {
	case at != "":
		title, content, deleted, err := contentAtIn(root, rel, at)
		if err != nil {
			http.Error(w, "Invalid commit", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"title":   title,
			"content": content,
			"deleted": deleted,
		})
	case from != "" && to != "":
		diff, err := diffBetweenIn(root, rel, from, to)
		if err != nil {
			http.Error(w, "Invalid commit range", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"diff": diff})
	default:
		revs, err := listRevisionsIn(root, rel)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(revs)
	}
}

func handleAPIGetViewState(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	if zeroRepoInstance() {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
		return
	}
	vsPath := filepath.Join(repo.paths.DevTop, "viewstate.json")
	data, err := os.ReadFile(vsPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func handleAPIPutViewState(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// A zero-repo instance keeps viewstate in memory: nothing is written to
	// the workspace until a repo is added.
	if zeroRepoInstance() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	vsPath := filepath.Join(repo.paths.DevTop, "viewstate.json")
	if err := os.WriteFile(vsPath, body, 0644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Favourites — Option B: user-scoped doc slugs, persisted next to the config
// in .devtop/favourites.json (never committed, like viewstate).
func handleAPIGetFavourites(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	slugs, err := listFavouritesP(repo.paths)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(slugs)
}

func handleAPIPutFavourites(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var slugs []string
	if err := json.Unmarshal(body, &slugs); err != nil {
		http.Error(w, "Expected a JSON array of slug strings", 400)
		return
	}
	if err := saveFavouritesP(repo.paths, slugs); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIDeleteDoc(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	slug := r.PathValue("slug")
	slug = strings.TrimSuffix(slug, ".mdx")
	if slug == "" || strings.Contains(slug, "..") {
		http.Error(w, "Invalid slug", 400)
		return
	}
	if slug == "index" || slug == "/" {
		http.Error(w, "Cannot delete the index document", 400)
		return
	}
	if err := deleteDocP(repo.paths, slug); err != nil {
		http.Error(w, "Document not found", 404)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
