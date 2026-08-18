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
		// The devtop root: the directory that contains (or will contain) the
		// user's project directories. Nothing here is created or scaffolded —
		// a project becomes managed when the user registers it and it gets
		// its own .devtop/.
		devtopEnv = "."
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

	// Repo registry: DEVTOP_REPOS=<root1>:<root2> serves multiple projects
	// from one instance; otherwise the persisted registry file carries them.
	// No DEVTOP_REPOS and no registry file means zero projects registered:
	// the UI shows the Add-project state. There is no single-repo fallback.
	if err := initRegistry(); err != nil {
		fmt.Printf("Warning: could not build repo registry: %v\n", err)
	}

	// Parse the engine config for the default repo (falls back to the bundled
	// default when no config.yml exists) so the legacy globals stay coherent.
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
	mux.HandleFunc("PUT /api/artifacts/{kind}/{id}", handleAPIArtifactUpdate)
	mux.HandleFunc("POST /api/pipeline/prospect", handleAPIProspect)
	mux.HandleFunc("POST /api/pipeline/prospect/classify", handleAPIProspectClassify)
	mux.HandleFunc("GET /api/artifacts/{kind}", handleAPIArtifacts)
	mux.HandleFunc("GET /api/artifacts/{kind}/{id...}", handleAPIArtifactDetail)
	mux.HandleFunc("POST /api/intents", handleAPIIntentCreate)
	mux.HandleFunc("POST /api/intents/{id}/publish", handleAPIWorkItemPublish)
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
	fmt.Printf("  Root:  %s\n", DEVTOP_DIR)
	if len(repos) == 0 {
		fmt.Printf("  Repos: 0 registered — add a project to begin\n")
	} else {
		fmt.Printf("  Repos: %d registered\n", len(repos))
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
