package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

type ToolHandler func(args map[string]interface{}) string

type ToolDef struct {
	Schema  map[string]interface{}
	Handler ToolHandler
}

var (
	toolRegistry   = make(map[string]ToolDef)
	toolRegistryMu sync.Mutex
	mcpToolSchemas []map[string]interface{}
)

const MAX_AGENT_DEPTH = 10

func init() {
	registerBuiltinTools()
}

func registerBuiltinTools() {
	registerTool("read_doc", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "read_doc",
			"description": "Read a documentation file from the docs/ directory.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Path relative to docs/ (e.g. 'architecture.mdx')"},
				},
				"required": []string{"path"},
			},
		},
	}, func(args map[string]interface{}) string {
		path, _ := args["path"].(string)
		filePath := filepath.Join(DOCS_DIR, path)
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Sprintf("Error: doc '%s' not found", path)
		}
		return string(data)
	})

	registerTool("write_doc", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "write_doc",
			"description": "Write or overwrite a documentation file in the docs/ directory. Use .mdx extension.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]interface{}{"type": "string", "description": "Path relative to docs/ (e.g. 'architecture.mdx')"},
					"content": map[string]interface{}{"type": "string", "description": "Full MDX content (YAML frontmatter + markdown body)"},
				},
				"required": []string{"path", "content"},
			},
		},
	}, func(args map[string]interface{}) string {
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		if err := writeDocToFileSystem(path, content); err != nil {
			return fmt.Sprintf("Error writing doc: %v", err)
		}
		return fmt.Sprintf("Written to docs/%s", path)
	})

	registerTool("list_docs", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "list_docs",
			"description": "List all available documentation files with their slugs and titles.",
			"parameters": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}, func(args map[string]interface{}) string {
		docs, err := listDocs()
		if err != nil {
			return fmt.Sprintf("Error listing docs: %v", err)
		}
		bytes, _ := json.MarshalIndent(docs, "", "  ")
		return string(bytes)
	})

	registerTool("list_tickets", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "list_tickets",
			"description": "List all tickets with their status, priority, and assignee.",
			"parameters": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}, func(args map[string]interface{}) string {
		tickets, err := listTickets()
		if err != nil {
			return fmt.Sprintf("Error listing tickets: %v", err)
		}
		bytes, _ := json.MarshalIndent(tickets, "", "  ")
		return string(bytes)
	})

	registerTool("read_ticket", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "read_ticket",
			"description": "Read a single ticket's full content including comments.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{"type": "string", "description": "Ticket ID (e.g. '001')"},
				},
				"required": []string{"id"},
			},
		},
	}, func(args map[string]interface{}) string {
		id, _ := args["id"].(string)
		filePath := filepath.Join(TICKETS_DIR, id+".md")
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Sprintf("Error: ticket '%s' not found", id)
		}
		return string(data)
	})

	registerTool("create_ticket", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "create_ticket",
			"description": "Create a new ticket.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":       map[string]interface{}{"type": "string", "description": "Ticket title"},
					"description": map[string]interface{}{"type": "string", "description": "Markdown description"},
					"priority":    map[string]interface{}{"type": "string", "enum": []string{"urgent", "high", "medium", "low"}, "description": "Priority level"},
					"assignee":    map[string]interface{}{"type": "string", "description": "Assignee username (optional)"},
				},
				"required": []string{"title", "description", "priority"},
			},
		},
	}, func(args map[string]interface{}) string {
		title, _ := args["title"].(string)
		desc, _ := args["description"].(string)
		priority, _ := args["priority"].(string)
		assignee, _ := args["assignee"].(string)

		tid := getNextTicketID()
		now := time.Now().UTC().Format("2006-01-02")
		t := Ticket{
			ID:             tid,
			Title:          title,
			Status:         "open",
			Priority:       priority,
			Assignee:       assignee,
			Created:        now,
			RawDescription: desc,
			Description:    renderMD(desc),
		}

		if err := writeTicketToFileSystem(t); err != nil {
			return fmt.Sprintf("Error creating ticket: %v", err)
		}
		return fmt.Sprintf("Created ticket %s: %s", tid, title)
	})

	registerTool("update_ticket", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "update_ticket",
			"description": "Update a ticket's status, priority, or assignee.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id":       map[string]interface{}{"type": "string", "description": "Ticket ID (e.g. '001')"},
					"status":   map[string]interface{}{"type": "string", "enum": []string{"open", "in-progress", "done", "wontfix"}, "description": "New status"},
					"priority": map[string]interface{}{"type": "string", "enum": []string{"urgent", "high", "medium", "low"}, "description": "New priority"},
					"assignee": map[string]interface{}{"type": "string", "description": "New assignee (empty string to unassign)"},
				},
				"required": []string{"id"},
			},
		},
	}, func(args map[string]interface{}) string {
		id, _ := args["id"].(string)
		t, err := getTicket(id)
		if err != nil {
			return fmt.Sprintf("Error: ticket '%s' not found", id)
		}

		if s, ok := args["status"].(string); ok && s != "" {
			t.Status = s
		}
		if p, ok := args["priority"].(string); ok && p != "" {
			t.Priority = p
		}
		if a, ok := args["assignee"].(string); ok {
			t.Assignee = a
		}

		if err := writeTicketToFileSystem(t); err != nil {
			return fmt.Sprintf("Error updating ticket: %v", err)
		}
		return fmt.Sprintf("Updated ticket %s", id)
	})

	registerTool("add_comment", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "add_comment",
			"description": "Add a comment to a ticket.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id":   map[string]interface{}{"type": "string", "description": "Ticket ID (e.g. '001')"},
					"body": map[string]interface{}{"type": "string", "description": "Comment text"},
				},
				"required": []string{"id", "body"},
			},
		},
	}, func(args map[string]interface{}) string {
		id, _ := args["id"].(string)
		body, _ := args["body"].(string)

		t, err := getTicket(id)
		if err != nil {
			return fmt.Sprintf("Error: ticket '%s' not found", id)
		}

		nowStr := time.Now().UTC().Format("2006-01-02 15:04")
		commentLine := fmt.Sprintf("\n**%s** — %s\n", nowStr, body)
		t.RawDescription += commentLine
		t.Description = renderMD(t.RawDescription)

		if err := writeTicketToFileSystem(t); err != nil {
			return fmt.Sprintf("Error adding comment: %v", err)
		}
		return fmt.Sprintf("Comment added to ticket %s", id)
	})

	registerTool("read_workspace_file", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "read_workspace_file",
			"description": "Read a text file from the workspace repository. Path is relative to the workspace root (e.g. 'README.md', 'src/main.go'). Use list_workspace_files to discover paths.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Path relative to the workspace root"},
				},
				"required": []string{"path"},
			},
		},
	}, func(args map[string]interface{}) string {
		rel, _ := args["path"].(string)
		if rel == "" {
			return "Error: path is required"
		}
		full, err := resolveWorkspacePath(rel)
		if err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		fi, err := os.Stat(full)
		if err != nil {
			return fmt.Sprintf("Error: file '%s' not found", rel)
		}
		if fi.IsDir() {
			return fmt.Sprintf("Error: '%s' is a directory — use list_workspace_files", rel)
		}
		if fi.Size() > MAX_WORKSPACE_READ_BYTES {
			return fmt.Sprintf("Error: file too large (%d bytes, max %d)", fi.Size(), MAX_WORKSPACE_READ_BYTES)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return fmt.Sprintf("Error reading file: %v", err)
		}
		if bytes.ContainsRune(data, '\x00') {
			return fmt.Sprintf("Binary file (%d bytes) — content not shown", len(data))
		}
		return string(data)
	})

	registerTool("list_workspace_files", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "list_workspace_files",
			"description": "List files and directories in the workspace repository. Path is relative to the workspace root; empty means the root. Skips .git, node_modules, and other generated directories.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Directory path relative to the workspace root (optional)"},
				},
			},
		},
	}, func(args map[string]interface{}) string {
		rel, _ := args["path"].(string)
		dir := workspaceRoot()
		if rel != "" {
			resolved, err := resolveWorkspacePath(rel)
			if err != nil {
				return fmt.Sprintf("Error: %v", err)
			}
			fi, err := os.Stat(resolved)
			if err != nil {
				return fmt.Sprintf("Error: '%s' not found", rel)
			}
			if !fi.IsDir() {
				return fmt.Sprintf("Error: '%s' is not a directory", rel)
			}
			dir = resolved
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Sprintf("Error listing directory: %v", err)
		}

		type entry struct {
			Name string `json:"name"`
			Type string `json:"type"`
			Size int64  `json:"size,omitempty"`
		}
		var out []entry
		for _, e := range entries {
			if ignoredWorkspaceDirs[e.Name()] {
				continue
			}
			if e.IsDir() {
				out = append(out, entry{Name: e.Name(), Type: "dir"})
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				out = append(out, entry{Name: e.Name(), Type: "symlink"})
				continue
			}
			out = append(out, entry{Name: e.Name(), Type: "file", Size: info.Size()})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Type != out[j].Type {
				return out[i].Type < out[j].Type
			}
			return out[i].Name < out[j].Name
		})
		b, _ := json.MarshalIndent(out, "", "  ")
		return string(b)
	})

	registerTool("git_commit", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "git_commit",
			"description": "Commit all changes made so far to git. Call this after every write/create/update operation. The message should describe what changed and why.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"message": map[string]interface{}{"type": "string", "description": "Commit message describing the changes"},
				},
				"required": []string{"message"},
			},
		},
	}, func(args map[string]interface{}) string {
		msg, _ := args["message"].(string)
		return gitCommit(msg)
	})

	registerTool("ask_user", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "ask_user",
			"description": "Ask the user a question and wait for their response. Use this when you need clarification, confirmation, or additional information from the user before proceeding.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"question": map[string]interface{}{"type": "string", "description": "The question to ask the user"},
				},
				"required": []string{"question"},
			},
		},
	}, func(args map[string]interface{}) string {
		question, _ := args["question"].(string)
		return fmt.Sprintf("__ASK_USER__:%s", question)
	})
}

func registerTool(name string, schema map[string]interface{}, handler ToolHandler) {
	toolRegistryMu.Lock()
	defer toolRegistryMu.Unlock()
	toolRegistry[name] = ToolDef{Schema: schema, Handler: handler}
}

const MAX_WORKSPACE_READ_BYTES = 512 * 1024

var ignoredWorkspaceDirs = map[string]bool{
	".git":         true,
	".devtop":      true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"target":       true,
	"vendor":       true,
	".idea":        true,
	".vscode":      true,
}

func workspaceRoot() string {
	return filepath.Dir(DEVTOP_DIR)
}

func pathWithinRoot(root, target string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func resolveWorkspacePath(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("use a path relative to the workspace root")
	}
	root := workspaceRoot()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if !pathWithinRoot(root, abs) {
		return "", fmt.Errorf("path escapes the workspace")
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		if !pathWithinRoot(root, real) {
			return "", fmt.Errorf("path escapes the workspace (symlink)")
		}
		return real, nil
	}
	return abs, nil
}

func registerMCPTool(name string, schema map[string]interface{}, handler ToolHandler) {
	toolRegistryMu.Lock()
	defer toolRegistryMu.Unlock()
	toolRegistry[name] = ToolDef{Schema: schema, Handler: handler}
	mcpToolSchemas = append(mcpToolSchemas, schema)
}

func getToolSchemas() []map[string]interface{} {
	toolRegistryMu.Lock()
	defer toolRegistryMu.Unlock()
	var schemas []map[string]interface{}
	for _, def := range toolRegistry {
		schemas = append(schemas, def.Schema)
	}
	return schemas
}

func dispatchTool(name string, args map[string]interface{}) string {
	toolRegistryMu.Lock()
	def, ok := toolRegistry[name]
	toolRegistryMu.Unlock()
	if !ok {
		return fmt.Sprintf("Unknown tool: %s", name)
	}
	return def.Handler(args)
}

func gitCommit(message string) string {
	repoDir := filepath.Dir(DEVTOP_DIR)
	rel, err := filepath.Rel(repoDir, DEVTOP_DIR)
	if err != nil {
		rel = DEVTOP_DIR
	}

	addCmd := exec.Command("git", "add", rel)
	addCmd.Dir = repoDir
	_ = addCmd.Run()

	commitCmd := exec.Command("git", "commit", "-m", message, "--allow-empty")
	commitCmd.Dir = repoDir
	var outBuf bytes.Buffer
	commitCmd.Stdout = &outBuf
	commitCmd.Stderr = &outBuf
	err = commitCmd.Run()

	out := strings.TrimSpace(outBuf.String())
	if err == nil {
		lines := strings.Split(out, "\n")
		if len(lines) > 0 {
			return "Committed: " + lines[0]
		}
		return "Committed successfully"
	}
	if strings.Contains(out, "nothing to commit") {
		return "Nothing to commit — no changes detected."
	}
	return out
}

type AgentMessage struct {
	Role       string                   `json:"role"`
	Content    string                   `json:"content"`
	Timestamp  string                   `json:"timestamp,omitempty"`
	ToolCalls  []map[string]interface{} `json:"tool_calls,omitempty"`
	ToolCallID string                   `json:"tool_call_id,omitempty"`
}

type AgentChunk struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Result    string `json:"result,omitempty"`
	Index     int    `json:"index,omitempty"`
}

const SYSTEM_PROMPT = `You are a helpful engineering assistant embedded in a project's documentation and ticket system.

You can read and write docs, manage tickets, read files in the workspace repository, and answer questions about the project.

When the user asks you to do something, use the appropriate tool. Always read before writing to understand the current state. Be concise but thorough.

When listing tickets, present them in a readable format with their ID, title, status, and priority.

If you need clarification or confirmation from the user, use the ask_user tool.

IMPORTANT: After every write, create, update, or comment operation, you MUST call git_commit() to record the change. The commit message should be descriptive, e.g. "docs: add architecture overview" or "tickets: update dk-001 status to in-progress".`

func loadAgentsPrompt() string {
	agentsFile := filepath.Join(APP_DIR, "AGENTS.md")
	if data, err := os.ReadFile(agentsFile); err == nil {
		return strings.TrimSpace(string(data))
	}
	return SYSTEM_PROMPT
}

func runAgent(ctx context.Context, messages []AgentMessage, apiKey, baseURL, model string, outChan chan<- AgentChunk) error {
	return runAgentWithDepth(ctx, messages, apiKey, baseURL, model, outChan, 0)
}

func runAgentWithDepth(ctx context.Context, messages []AgentMessage, apiKey, baseURL, model string, outChan chan<- AgentChunk, depth int) error {
	defer close(outChan)

	if depth >= MAX_AGENT_DEPTH {
		outChan <- AgentChunk{
			Type:    "text",
			Content: "Maximum agent call depth reached. Stopping to prevent infinite loop.",
		}
		return fmt.Errorf("max agent depth %d reached", MAX_AGENT_DEPTH)
	}

	systemMsg := AgentMessage{Role: "system", Content: loadAgentsPrompt()}
	fullMessages := append([]AgentMessage{systemMsg}, messages...)

	// Map AgentMessage to openai.ChatCompletionMessage
	openAIMessages := make([]openai.ChatCompletionMessage, len(fullMessages))
	for i, m := range fullMessages {
		var toolCalls []openai.ToolCall
		if len(m.ToolCalls) > 0 {
			tcBytes, err := json.Marshal(m.ToolCalls)
			if err == nil {
				_ = json.Unmarshal(tcBytes, &toolCalls)
			}
		}
		openAIMessages[i] = openai.ChatCompletionMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  toolCalls,
			ToolCallID: m.ToolCallID,
		}
	}

	// Map Tool Schemas to openai.Tool
	var tools []openai.Tool
	schemas := getToolSchemas()
	if len(schemas) > 0 {
		schemasBytes, err := json.Marshal(schemas)
		if err == nil {
			_ = json.Unmarshal(schemasBytes, &tools)
		}
	}

	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	client := openai.NewClientWithConfig(config)

	req := openai.ChatCompletionRequest{
		Model:     model,
		Messages:  openAIMessages,
		Tools:     tools,
		Stream:    true,
		MaxTokens: 4096,
	}

	stream, err := client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return err
	}
	defer stream.Close()

	type accumulatedToolCall struct {
		ID        string
		Name      string
		Arguments string
		Index     int
	}
	var toolCalls = make(map[int]*accumulatedToolCall)

	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		if len(response.Choices) == 0 {
			continue
		}

		choice := response.Choices[0]
		if choice.Delta.Content != "" {
			outChan <- AgentChunk{Type: "text", Content: choice.Delta.Content}
		}

		for _, tc := range choice.Delta.ToolCalls {
			idx := 0
			if tc.Index != nil {
				idx = *tc.Index
			}
			if _, ok := toolCalls[idx]; !ok {
				toolCalls[idx] = &accumulatedToolCall{
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
					Index:     idx,
				}
			} else {
				if tc.ID != "" {
					toolCalls[idx].ID = tc.ID
				}
				if tc.Function.Name != "" {
					toolCalls[idx].Name = tc.Function.Name
				}
				toolCalls[idx].Arguments += tc.Function.Arguments
			}
		}
	}

	if len(toolCalls) > 0 {
		var followupMessages []AgentMessage
		followupMessages = append(followupMessages, messages...)

		var assistToolCalls []map[string]interface{}
		for _, tc := range toolCalls {
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("call_%s_%d", tc.Name, tc.Index)
			}
			assistToolCalls = append(assistToolCalls, map[string]interface{}{
				"id":   id,
				"type": "function",
				"function": map[string]interface{}{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			})
		}
		followupMessages = append(followupMessages, AgentMessage{
			Role:      "assistant",
			Content:   "",
			ToolCalls: assistToolCalls,
		})

		for _, tc := range toolCalls {
			var args map[string]interface{}
			_ = json.Unmarshal([]byte(tc.Arguments), &args)
			if args == nil {
				args = make(map[string]interface{})
			}

			result := dispatchTool(tc.Name, args)
			outChan <- AgentChunk{
				Type:      "tool_call",
				Name:      tc.Name,
				Arguments: tc.Arguments,
				Result:    result,
				Index:     tc.Index,
			}

			followupMessages = append(followupMessages, AgentMessage{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		followupChan := make(chan AgentChunk, 100)
		go func() {
			_ = runAgentWithDepth(ctx, followupMessages, apiKey, baseURL, model, followupChan, depth+1)
		}()

		for chunk := range followupChan {
			outChan <- chunk
		}
	}

	return nil
}

func generateThreadTitle(ctx context.Context, firstUserMsg, apiKey, baseURL, model string) (string, error) {
	config := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		config.BaseURL = baseURL
	}
	client := openai.NewClientWithConfig(config)

	prompt := "You are a helpful assistant. Generate a short, concise, 3-5 word title summary for a chat that starts with the following user message. Do not use quotes, punctuation, or markdown formatting.\n\nUser message: " + firstUserMsg

	req := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    "user",
				Content: prompt,
			},
		},
		MaxTokens: 20,
	}

	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) > 0 {
		title := strings.TrimSpace(resp.Choices[0].Message.Content)
		title = strings.Trim(title, `"'`)
		if len(title) > 40 {
			title = title[:40] + "..."
		}
		return title, nil
	}

	return "", fmt.Errorf("no choice returned")
}
