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
	// Sandboxed tool execution: the parent re-executes this binary as
	// `devtop-bin tool-run` under Landlock; run the tool and exit before any
	// server state is built.
	if os.Getenv("DEVTOP_TOOL_SANDBOX_RUN") == "1" && len(os.Args) > 1 && os.Args[1] == "tool-run" {
		os.Exit(runToolSubprocess())
	}

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

	// Repo registry: DEVTOP_REPOS=<root1>:<root2> serves multiple repos from
	// one instance. Unset means the classic single-repo mode (no repo param).
	if err := initRegistry(); err != nil {
		fmt.Printf("Warning: could not build repo registry: %v\n", err)
	}

	// The classic single-repo workspace is seeded at boot so the docs view
	// has content on first run. In multi-repo mode (and on a fresh launch of
	// a folder-of-repos mount with zero repos) the default workspace stays
	// untouched until a repo is explicitly initialized.
	if registryHasSynthetic() {
		// Complete scaffold: storage dirs, config.yml, kind dirs, the
		// default agents and skills, and the welcome doc. Non-destructive.
		if err := scaffoldRepo(defaultPaths()); err != nil {
			fmt.Printf("Warning: could not scaffold the default workspace: %v\n", err)
		}
	}
	// Parse the engine config in both modes (falls back to the bundled
	// default when no config.yml exists).
	if err := loadEngineConfig(); err != nil {
		fmt.Printf("Warning: invalid engine config: %v\n", err)
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
	mux.HandleFunc("GET /api/revisions/docs/{slug...}", handleAPIDocRevisions)
	mux.HandleFunc("GET /api/revisions/tickets/{id}", handleAPITicketRevisions)
	mux.HandleFunc("GET /api/threads", handleAPIThreads)
	mux.HandleFunc("POST /api/threads", handleAPICreateThread)
	mux.HandleFunc("GET /api/threads/{id}", handleAPIGetThread)
	mux.HandleFunc("DELETE /api/threads/{id}", handleAPIDeleteThread)
	mux.HandleFunc("POST /api/chat/{thread_id}", handleAPIChat)
	mux.HandleFunc("GET /api/models", handleAPIModels)
	mux.HandleFunc("GET /api/config", handleAPIConfig)
	mux.HandleFunc("GET /api/engine-config", handleAPIEngineConfig)
	mux.HandleFunc("GET /api/pipeline", handleAPIPipeline)
	mux.HandleFunc("POST /api/derive", handleAPIDerive)
	mux.HandleFunc("POST /api/artifacts/{kind}/{id}/review", handleAPIArtifactReview)
	mux.HandleFunc("POST /api/pipeline/prospect", handleAPIProspect)
	mux.HandleFunc("POST /api/pipeline/prospect/classify", handleAPIProspectClassify)
	mux.HandleFunc("GET /api/artifacts/{kind}", handleAPIArtifacts)
	mux.HandleFunc("GET /api/artifacts/{kind}/{id...}", handleAPIArtifactDetail)
	mux.HandleFunc("POST /api/intents", handleAPIIntentCreate)
	mux.HandleFunc("GET /api/viewstate", handleAPIGetViewState)
	mux.HandleFunc("PUT /api/viewstate", handleAPIPutViewState)
	mux.HandleFunc("GET /api/favourites", handleAPIGetFavourites)
	mux.HandleFunc("PUT /api/favourites", handleAPIPutFavourites)
	mux.HandleFunc("DELETE /api/docs/{slug...}", handleAPIDeleteDoc)

	// Repo scope: registry, init, and the folder browser for Add repo.
	mux.HandleFunc("GET /api/repos", handleAPIRepos)
	mux.HandleFunc("POST /api/repos", handleAPIRepos)
	mux.HandleFunc("GET /api/repos/{name}", handleAPIRepoDetail)
	mux.HandleFunc("DELETE /api/repos/{name}", handleAPIRepoDelete)
	mux.HandleFunc("POST /api/repos/init", handleAPIRepoInit)
	mux.HandleFunc("POST /api/internal/tool", handleAPIInternalTool)
	mux.HandleFunc("GET /api/agent", handleAPIAgent)
	mux.HandleFunc("GET /api/workspace/revision", handleAPIWorkspaceRevision)
	mux.HandleFunc("/api/fs/list", handleAPIFSList)

	// SPA routes — serve the built React app; any non-API path falls back to
	// index.html (React hash routing handles the rest).
	mux.HandleFunc("/{path...}", handleSPA)

	addr := fmt.Sprintf("%s:%d", *host, *port)
	fmt.Printf("  devtop — Go local dev server\n")
	repos := registry.List()
	if len(repos) == 1 && repos[0].Single {
		fmt.Printf("  Data: %s\n", DATA_DIR)
	} else {
		fmt.Printf("  Repos: %d registered (DEVTOP_REPOS)\n", len(repos))
		for _, r := range repos {
			fmt.Printf("    - %s (%s)\n", r.Name, r.Root)
		}
	}
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

	// Watch every registered repo's storage dirs.
	for _, repo := range registry.List() {
		addDirRecursive(watcher, repo.paths.Docs)
		addDirRecursive(watcher, repo.paths.Tickets)
		addDirRecursive(watcher, repo.paths.Threads)
	}

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
