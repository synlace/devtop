package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

var (
	APP_DIR     string
	STATIC_DIR  string
	DEVTOP_DIR  string
	DOCS_DIR    string
	TICKETS_DIR string
	THREADS_DIR string
	DATA_DIR    string
)

func loadDotEnv() {
	raw, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			continue
		}
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func main() {
	port := flag.Int("port", 8000, "Port to listen on")
	host := flag.String("host", "127.0.0.1", "Address to listen on (0.0.0.0 in containers)")
	flag.Parse()

	loadDotEnv()

	// Setup directories
	var err error
	execPath, err := os.Executable()
	if err != nil {
		APP_DIR = "."
	} else {
		APP_DIR = filepath.Dir(execPath)
		// Fallback for go run
		if strings.Contains(APP_DIR, "go-build") || strings.Contains(APP_DIR, "Temp") {
			APP_DIR = "."
		}
	}

	// We can override APP_DIR via environment variable if running tests or customized directory
	if envAppDir := os.Getenv("APP_DIR"); envAppDir != "" {
		APP_DIR = envAppDir
	}

	STATIC_DIR = filepath.Join(APP_DIR, "frontend", "dist")

	devtopEnv := os.Getenv("DEVTOP_DIR")
	if devtopEnv == "" {
		devtopEnv = "./.devtop"
	}
	if !filepath.IsAbs(devtopEnv) {
		cwd, _ := os.Getwd()
		DEVTOP_DIR = filepath.Join(cwd, devtopEnv)
	} else {
		DEVTOP_DIR = devtopEnv
	}

	DOCS_DIR = filepath.Join(DEVTOP_DIR, "docs")
	TICKETS_DIR = filepath.Join(DEVTOP_DIR, "tickets")
	THREADS_DIR = filepath.Join(DEVTOP_DIR, "threads")
	DATA_DIR = filepath.Join(DEVTOP_DIR, "data")

	// Ensure directories exist
	_ = os.MkdirAll(DOCS_DIR, 0755)
	_ = os.MkdirAll(TICKETS_DIR, 0755)
	_ = os.MkdirAll(THREADS_DIR, 0755)
	_ = os.MkdirAll(DATA_DIR, 0755)

	// Materialize the bundled engine config on first run, then parse it.
	// A repo that commits its own config.yml overrides the default.
	if _, err := ensureEngineConfig(); err != nil {
		fmt.Printf("Warning: could not materialize engine config: %v\n", err)
	}
	if err := loadEngineConfig(); err != nil {
		fmt.Printf("Warning: invalid engine config: %v\n", err)
	}
	// Create directories for every config-declared kind (prds, any future
	// kind) so the generic artifact endpoints work on a fresh repo.
	if err := ensureKindDirs(); err != nil {
		fmt.Printf("Warning: could not create artifact kind dirs: %v\n", err)
	}
	// Fresh repos get a materialized welcome doc so the docs view has content
	// (and the AI can read it). Non-destructive: skipped when docs exist.
	if err := ensureWelcomeDoc(); err != nil {
		fmt.Printf("Warning: could not create welcome doc: %v\n", err)
	}

	// Connect MCP servers
	mcpCfgs := parseMCPServers()
	if len(mcpCfgs) > 0 {
		ctx := context.Background()
		conns := connectMCPServers(ctx, mcpCfgs)
		mcpMu.Lock()
		mcpConnections = conns
		mcpMu.Unlock()
		for _, conn := range conns {
			for _, tool := range conn.Tools {
				serverName := conn.ServerName
				c := conn.Client
				toolName := tool.Name
				schema := mcpToolToSchema(tool)
				registerMCPTool(toolName, schema, func(args map[string]interface{}) string {
					return callMCPTool(serverName, c, toolName, args)
				})
			}
		}
	}

	// Start filesystem watcher
	go startFileWatcher()

	// Define Routes using Go 1.22+ routing features
	mux := http.NewServeMux()

	// CopilotKit runtime proxy (forwarded to the Node runtime)
	mux.HandleFunc("/api/copilotkit", handleCopilotKitProxy)
	mux.HandleFunc("/api/copilotkit/", handleCopilotKitProxy)

	// API Handlers
	mux.HandleFunc("GET /api/docs", handleAPIDocs)
	mux.HandleFunc("GET /api/docs/{slug...}", handleAPIDocPage)
	mux.HandleFunc("GET /api/tickets", handleAPITickets)
	mux.HandleFunc("GET /api/tickets/{id}", handleAPITicketDetail)
	mux.HandleFunc("GET /api/threads", handleAPIThreads)
	mux.HandleFunc("POST /api/threads", handleAPICreateThread)
	mux.HandleFunc("GET /api/threads/{id}", handleAPIGetThread)
	mux.HandleFunc("DELETE /api/threads/{id}", handleAPIDeleteThread)
	mux.HandleFunc("POST /api/chat/{thread_id}", handleAPIChat)
	mux.HandleFunc("GET /api/models", handleAPIModels)
	mux.HandleFunc("GET /api/config", handleAPIConfig)
	mux.HandleFunc("GET /api/engine-config", handleAPIEngineConfig)
	mux.HandleFunc("GET /api/artifacts/{kind}", handleAPIArtifacts)
	mux.HandleFunc("GET /api/artifacts/{kind}/{id...}", handleAPIArtifactDetail)
	mux.HandleFunc("GET /api/viewstate", handleAPIGetViewState)
	mux.HandleFunc("PUT /api/viewstate", handleAPIPutViewState)

	// SPA routes — serve the built React app; any non-API path falls back to
	// index.html (React hash routing handles the rest).
	mux.HandleFunc("/{path...}", handleSPA)

	addr := fmt.Sprintf("%s:%d", *host, *port)
	fmt.Printf("  devtop — Go local dev server\n")
	fmt.Printf("  Data: %s\n", DEVTOP_DIR)
	fmt.Printf("  URL:  http://%s\n\n", addr)
	if _, err := os.Stat(filepath.Join(STATIC_DIR, "index.html")); err == nil {
		fmt.Printf("  Frontend: serving %s\n", STATIC_DIR)
	} else {
		fmt.Printf("  Frontend: not built (run `just devtop build`, or use `just devtop dev`)\n")
	}

	log.Fatal(http.ListenAndServe(addr, mux))
}

func addDirRecursive(watcher *fsnotify.Watcher, root string) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			_ = watcher.Add(path)
		}
		return nil
	})
}

func startFileWatcher() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Printf("Warning: failed to start file watcher: %v\n", err)
		return
	}
	defer watcher.Close()

	addDirRecursive(watcher, DOCS_DIR)
	addDirRecursive(watcher, TICKETS_DIR)
	addDirRecursive(watcher, THREADS_DIR)

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				ext := filepath.Ext(event.Name)
				if ext == ".mdx" || ext == ".md" || ext == ".json" {
					// Files are the source of truth — no DB sync needed
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Printf("File watcher error: %v\n", err)
		}
	}
}
