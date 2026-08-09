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
		AgentPrompt: loadAgentsPrompt(),
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
	docs, err := listDocs()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(docs)
}

func handleAPIDocPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	slug = strings.TrimSuffix(slug, ".mdx")

	title, htmlContent, err := getDoc(slug)
	if err != nil {
		if slug == "index" {
			docs, _ := listDocs()
			if len(docs) > 0 {
				title, htmlContent, err = getDoc(docs[0].Slug)
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
	tickets, err := listTickets()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tickets)
}

func handleAPITicketDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := getTicket(id)
	if err != nil {
		http.Error(w, "Ticket not found", 404)
		return
	}

	t.Comments = extractComments(t.RawDescription)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

func handleAPIThreads(w http.ResponseWriter, r *http.Request) {
	context := r.URL.Query().Get("context")
	threads, err := listThreads(context)
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

	if err := writeThreadToFileSystem(tid, threadMap); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(threadMap)
}

func handleAPIGetThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := getThread(id)
	if err != nil {
		http.Error(w, "Thread not found", 404)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(t)
}

func handleAPIDeleteThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Thread ID is required", 400)
		return
	}
	if err := deleteThreadFile(id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIChat(w http.ResponseWriter, r *http.Request) {
	threadID := r.PathValue("thread_id")
	threadMap, err := getThread(threadID)
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

	_ = writeThreadToFileSystem(threadID, threadMap)

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
	go func() {
		_ = runAgent(context.Background(), agentMsgs, cfg.APIKey, cfg.BaseURL, cfg.Model, outChan)
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

		_ = writeThreadToFileSystem(threadID, threadMap)
	}

	doneBytes, _ := json.Marshal(map[string]string{"type": "done"})
	fmt.Fprintf(w, "data: %s\n\n", string(doneBytes))
	flusher.Flush()
}

func handleAPIModels(w http.ResponseWriter, r *http.Request) {
	cfg := getAPIConfig()
	models, _ := fetchModels(cfg.BaseURL, cfg.APIKey)
	resp := map[string]interface{}{
		"models": models,
		"config": cfg,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleAPIConfig(w http.ResponseWriter, r *http.Request) {
	cfg := getAPIConfig()
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

func handleAPIGetViewState(w http.ResponseWriter, r *http.Request) {
	vsPath := filepath.Join(DEVTOP_DIR, "viewstate.json")
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	vsPath := filepath.Join(DEVTOP_DIR, "viewstate.json")
	if err := os.WriteFile(vsPath, body, 0644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
