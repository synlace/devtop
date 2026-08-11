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
		filePath, err := docPathForSlugP(toolPaths(), path)
		if err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Sprintf("Error: doc '%s' not found", path)
		}
		return string(data)
	})

	registerTool("read_doc_at", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "read_doc_at",
			"description": "Read a documentation file as it existed at a given git commit. Commit may be a full or short sha, or HEAD. Returns the doc at that revision, or notes the file was deleted there.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":   map[string]interface{}{"type": "string", "description": "Path relative to docs/ (e.g. 'architecture.mdx')"},
					"commit": map[string]interface{}{"type": "string", "description": "Git commit sha or HEAD"},
				},
				"required": []string{"path", "commit"},
			},
		},
	}, func(args map[string]interface{}) string {
		path, _ := args["path"].(string)
		commit, _ := args["commit"].(string)
		filePath, err := docPathForSlugP(toolPaths(), path)
		if err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		title, content, deleted, err := contentAt(filePath, commit)
		if err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		if deleted {
			return fmt.Sprintf("docs/%s was deleted at commit %s", path, commit)
		}
		return fmt.Sprintf("title: %s\n%s", title, content)
	})

	registerTool("list_doc_revisions", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "list_doc_revisions",
			"description": "List the git history of a documentation file: each commit that changed it, newest first, with sha, message, author, and date. The most recent is marked current.",
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
		filePath, err := docPathForSlugP(toolPaths(), path)
		if err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		revs, err := listRevisions(filePath)
		if err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		bytes, _ := json.MarshalIndent(revs, "", "  ")
		return string(bytes)
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

		// Creating a new doc under a *different* slug than the one the file
		// actually lives at silently forks content. When the requested slug
		// does not yet exist but another doc already has the same title,
		// refuse and point the agent at the existing slug.
		p := toolPaths()
		if _, err := docPathForSlugP(p, path); err != nil {
			if title := docTitleFromContent(content); title != "" {
				docs, _ := listDocsP(p)
				normPath := strings.TrimSuffix(path, ".mdx")
				for _, d := range docs {
					if strings.EqualFold(d.Title, title) && d.Slug != normPath {
						return fmt.Sprintf("Error: a doc titled %q already exists at docs/%s. Use write_doc path=%q to update it instead of creating a duplicate.", title, d.Slug, d.Slug)
					}
				}
			}
		}

		if err := writeDocToFileSystemP(p, path, content); err != nil {
			return fmt.Sprintf("Error writing doc: %v", err)
		}
		return fmt.Sprintf("Written to docs/%s", path)
	})

	registerTool("write_artifact", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "write_artifact",
			"description": "Write or overwrite an artifact of a config-declared, agent-writable kind (e.g. prds). Content includes YAML frontmatter and a markdown body.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"kind":    map[string]interface{}{"type": "string", "description": "Artifact kind, declared in config.yml (e.g. 'prds')"},
					"id":      map[string]interface{}{"type": "string", "description": "Artifact id, e.g. 'architecture/migrations'"},
					"content": map[string]interface{}{"type": "string", "description": "Full file content (YAML frontmatter + markdown body)"},
				},
				"required": []string{"kind", "id", "content"},
			},
		},
	}, func(args map[string]interface{}) string {
		kind, _ := args["kind"].(string)
		id, _ := args["id"].(string)
		content, _ := args["content"].(string)

		cfg := toolConfig()
		p := toolPaths()
		k, ok := cfg.ArtifactKinds[kind]
		if !ok || !k.AgentWritable {
			return fmt.Sprintf("Error: kind %q is not an agent-writable artifact kind", kind)
		}
		// Board kinds have a typed write flow; never route through generic writes.
		if k.View == "board" {
			return fmt.Sprintf("Error: kind %q has a typed write flow; use its dedicated tool", kind)
		}
		rel, ok := resolveArtifactWriteTargetFor(cfg, p, kind, id)
		if !ok {
			return "Error: cannot determine target path for write_artifact"
		}
		if _, err := guardPath(p.DevTop, rel); err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		path := filepath.Join(p.DevTop, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Sprintf("Error writing artifact: %v", err)
		}
		return fmt.Sprintf("Written to .devtop/%s", rel)
	})

	registerTool("read_artifact", map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "read_artifact",
			"description": "Read an artifact of any config-declared kind (e.g. 'prds', 'docs', 'tickets') from the repository's .devtop/ directory. Use this for kinds without a dedicated reader.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"kind": map[string]interface{}{"type": "string", "description": "Artifact kind, declared in config.yml (e.g. 'prds')"},
					"id":   map[string]interface{}{"type": "string", "description": "Artifact id, e.g. 'architecture/migrations'"},
				},
				"required": []string{"kind", "id"},
			},
		},
	}, func(args map[string]interface{}) string {
		kind, _ := args["kind"].(string)
		id, _ := args["id"].(string)

		p := toolPaths()
		cfg := toolConfig()
		rel, ok := resolveArtifactReadTargetFor(cfg, p, kind, id)
		if !ok {
			return fmt.Sprintf("Error: artifact '%s/%s' not found", kind, id)
		}
		if _, err := guardPath(p.DevTop, rel); err != nil {
			return fmt.Sprintf("Error: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(p.DevTop, rel))
		if err != nil {
			return fmt.Sprintf("Error: artifact '%s/%s' not found", kind, id)
		}
		return string(data)
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
		docs, err := listDocsP(toolPaths())
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
		tickets, err := listTicketsP(toolPaths())
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
		filePath := filepath.Join(toolPaths().Tickets, id+".md")
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
					"source":      map[string]interface{}{"type": "string", "description": "Origin artifact slug, e.g. prds/data-layer (optional)"},
				},
				"required": []string{"title", "description", "priority"},
			},
		},
	}, func(args map[string]interface{}) string {
		title, _ := args["title"].(string)
		desc, _ := args["description"].(string)
		priority, _ := args["priority"].(string)
		assignee, _ := args["assignee"].(string)
		source, _ := args["source"].(string)

		tid := getNextTicketIDP(toolPaths())
		now := time.Now().UTC().Format("2006-01-02")
		t := Ticket{
			ID:             tid,
			Title:          title,
			Status:         "open",
			Priority:       priority,
			Assignee:       assignee,
			Created:        now,
			Source:         source,
			RawDescription: desc,
			Description:    renderMD(desc),
		}

		if err := writeTicketToFileSystemP(toolPaths(), t); err != nil {
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
		t, err := getTicketP(toolPaths(), id)
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

		if err := writeTicketToFileSystemP(toolPaths(), t); err != nil {
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

		t, err := getTicketP(toolPaths(), id)
		if err != nil {
			return fmt.Sprintf("Error: ticket '%s' not found", id)
		}

		nowStr := time.Now().UTC().Format("2006-01-02 15:04")
		commentLine := fmt.Sprintf("\n**%s** — %s\n", nowStr, body)
		t.RawDescription += commentLine
		t.Description = renderMD(t.RawDescription)

		if err := writeTicketToFileSystemP(toolPaths(), t); err != nil {
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
		full, err := resolveWorkspacePathIn(toolWorkspaceRoot(), rel)
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
		dir := toolWorkspaceRoot()
		if rel != "" {
			resolved, err := resolveWorkspacePathIn(dir, rel)
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
		return gitCommitIn(currentToolRepo(), msg)
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
	return resolveWorkspacePathIn(workspaceRoot(), rel)
}

// guardPath resolves a tool-supplied relative path against root, rejecting
// absolute paths, traversal, and symlink escapes. The deepest existing
// ancestor is resolved first so symlinked parents of a not-yet-created file
// are caught too.
func guardPath(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("use a path relative to the root")
	}
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if !pathWithinRoot(root, abs) {
		return "", fmt.Errorf("path escapes the root")
	}
	probe := abs
	var rest []string
	for {
		if real, err := filepath.EvalSymlinks(probe); err == nil {
			full := real
			for i := len(rest) - 1; i >= 0; i-- {
				full = filepath.Join(full, rest[i])
			}
			if !pathWithinRoot(root, full) {
				return "", fmt.Errorf("path escapes the root (symlink)")
			}
			return full, nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return abs, nil
		}
		rest = append(rest, filepath.Base(probe))
		probe = parent
	}
}

// resolveWorkspacePathIn resolves a tool path relative to the given root,
// guarding traversal (including via symlinks).
func resolveWorkspacePathIn(root, rel string) (string, error) {
	return guardPath(root, rel)
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

// toolCtx carries the repo a tool dispatch is scoped to. Tool handlers read
// the package globals (legacy single-repo behavior) unless a dispatch pins the
// current repo; the pin is held for the duration of one handler call, so
// concurrent agents in different repos never cross scopes.
var toolCtx struct {
	mu   sync.Mutex
	repo *Repo
}

// dispatchToolForRepo runs a tool under an agent runtime's allowlist and
// permission scopes, scoped to repo (nil = legacy global behavior).
func dispatchToolForRepo(repo *Repo, rt *agentRuntime, name string, args map[string]interface{}) string {
	if rt != nil {
		if msg := rt.authorizeTool(name, args); msg != "" {
			return msg
		}
	}
	toolRegistryMu.Lock()
	def, ok := toolRegistry[name]
	toolRegistryMu.Unlock()
	if !ok {
		return fmt.Sprintf("Unknown tool: %s", name)
	}
	toolCtx.mu.Lock()
	prev := toolCtx.repo
	toolCtx.repo = repo
	result := def.Handler(args)
	toolCtx.repo = prev
	toolCtx.mu.Unlock()
	if _, isWrite := writeToolPathers[name]; isWrite {
		var root string
		if repo != nil {
			root = repo.Root
		} else {
			root = filepath.Dir(DEVTOP_DIR)
		}
		bumpWorkspaceRevision(root)
	}
	return result
}

// currentToolRepo returns the repo scoped to the running tool call, or nil.
// The caller must be inside a tool handler (dispatchToolForRepo holds the
// lock); reads in a critical section are ordered against the write by the
// same mutex, so this is race-safe without re-locking.
func currentToolRepo() *Repo {
	return toolCtx.repo
}

// toolPaths returns the storage paths of the scoped repo, or the legacy
// globals when the call is unscoped.
func toolPaths() RepoPaths {
	if r := currentToolRepo(); r != nil {
		return r.paths
	}
	return defaultPaths()
}

// toolConfig returns the config of the scoped repo, or the legacy global.
func toolConfig() EngineConfig {
	if r := currentToolRepo(); r != nil {
		if cfg, err := r.Config(); err == nil {
			return cfg
		}
	}
	return engineConfig
}

// toolWorkspaceRoot returns the workspace root of the scoped repo.
func toolWorkspaceRoot() string {
	if r := currentToolRepo(); r != nil {
		return r.Root
	}
	return workspaceRoot()
}

func dispatchTool(name string, args map[string]interface{}) string {
	return dispatchToolResolved(nil, name, args)
}

func gitCommitIn(repo *Repo, message string) string {
	if repo == nil {
		return gitCommit(message)
	}
	p := repo.paths
	repoDir := gitRootFrom(p.DevTop)
	if repoDir == "" {
		repoDir = repo.Root
	}
	rel, err := filepath.Rel(repoDir, p.DevTop)
	if err != nil {
		rel = p.DevTop
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

func gitCommit(message string) string {
	return gitCommitIn(forRepoOf(defaultPaths()), message)
}

// forRepoOf finds the registered repo whose devtop dir matches p, or a
// synthetic legacy repo so unscoped writes keep the classic single-repo
// behavior (tests, startup scaffolding).
func forRepoOf(p RepoPaths) *Repo {
	for _, r := range registry.List() {
		if r.paths.DevTop == p.DevTop {
			return r
		}
	}
	return &Repo{
		Name:  repoNameForRoot(p.DevTop),
		Root:  filepath.Dir(p.DevTop),
		Dir:   p.DevTop,
		paths: p,
	}
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

func runAgent(ctx context.Context, messages []AgentMessage, apiKey, baseURL, model string, rt *agentRuntime, outChan chan<- AgentChunk) error {
	return runAgentInRepo(ctx, nil, messages, apiKey, baseURL, model, rt, outChan)
}

// runAgentInRepo runs the agent scoped to a repo (nil = legacy globals). Tool
// dispatches inside the agent loop are scoped to the repo, so docs, tickets,
// workspace files and git commits always land in the owning repository. The
// runtime must be non-nil: prompts come exclusively from a repo-owned agent —
// there is no built-in fallback.
func runAgentInRepo(ctx context.Context, repo *Repo, messages []AgentMessage, apiKey, baseURL, model string, rt *agentRuntime, outChan chan<- AgentChunk) error {
	if rt == nil {
		return fmt.Errorf("no agent configured: initialize the repo to scaffold .devtop/agents")
	}
	prompt := rt.prompt
	if prompt == "" {
		prompt = buildAgentPrompt(rt)
	}
	return runAgentWithDepth(ctx, repo, messages, apiKey, baseURL, model, prompt, rt, outChan, 0)
}

func runAgentWithDepth(ctx context.Context, repo *Repo, messages []AgentMessage, apiKey, baseURL, model, prompt string, rt *agentRuntime, outChan chan<- AgentChunk, depth int) error {
	defer close(outChan)

	if depth >= MAX_AGENT_DEPTH {
		outChan <- AgentChunk{
			Type:    "text",
			Content: "Maximum agent call depth reached. Stopping to prevent infinite loop.",
		}
		return fmt.Errorf("max agent depth %d reached", MAX_AGENT_DEPTH)
	}

	systemMsg := AgentMessage{Role: "system", Content: prompt}
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

			result := dispatchToolForRepo(repo, rt, tc.Name, args)
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
			_ = runAgentWithDepth(ctx, repo, followupMessages, apiKey, baseURL, model, prompt, rt, followupChan, depth+1)
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
