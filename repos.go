package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v2"
)

// Repo scope: every devtop instance serves a set of project directories (the
// registry), and every API request resolves its active repo explicitly. There
// is no single-repo mode: a project is a directory that owns a .devtop/
// (usually a git checkout), registered by hand or by path. The registry is the
// only instance-level state — repos never know which instance serves them.
//
// Layout (standard): <project>/.devtop manages the repo; docs, tickets,
// threads and data live there. The devtop root (DEVTOP_DIR) is the directory
// the user points the instance at — typically the mounted volume — used for
// the folder browser and as a default anchor for registering projects.

// RepoPaths are the storage directories of one repo.
type RepoPaths struct {
	DevTop  string
	Docs    string
	Tickets string
	Threads string
	Data    string
}

func newRepoPaths(devTop string) RepoPaths {
	return RepoPaths{
		DevTop:  devTop,
		Docs:    filepath.Join(devTop, "docs"),
		Tickets: filepath.Join(devTop, "tickets"),
		Threads: filepath.Join(devTop, "threads"),
		Data:    filepath.Join(devTop, "data"),
	}
}

// Repo is the per-project context: its devtop dir, storage paths, git status,
// and lazily-parsed engine config.
type Repo struct {
	Name        string    `json:"name"`
	Root        string    `json:"path"`   // project root (workspace root)
	Dir         string    `json:"devtop"` // .devtop directory
	paths       RepoPaths `json:"-"`
	cfg         EngineConfig
	cfgErr      error
	cfgOnce     sync.Once
	cfgMu       sync.Mutex
	initialized bool
}

// RepoStatus is what the UI needs to render the switcher and overview.
type RepoStatus struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	Status      string `json:"status"` // ready | dirty | uninit | nogit
	Dirty       int    `json:"dirty,omitempty"`
	Docs        int    `json:"docs"`
	Initialized bool   `json:"initialized"`
	HasGit      bool   `json:"has_git"`
	Pending     int    `json:"pending"`
}

// defaultPaths returns the paths of the default (first) registered project,
// or the package globals when the registry is empty (hermetic tests and
// legacy shims that predate repo scoping).
func defaultPaths() RepoPaths {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if len(registry.repos) > 0 {
		return registry.repos[0].paths
	}
	return RepoPaths{
		DevTop:  DEVTOP_DIR,
		Docs:    DOCS_DIR,
		Tickets: TICKETS_DIR,
		Threads: THREADS_DIR,
		Data:    DATA_DIR,
	}
}

// gitRootFrom locates the git repository that owns devTopDir, mirroring
// findRepoRoot's convention: the parent (root/.devtop) or the dir itself.
func gitRootFrom(devTopDir string) string {
	abs, err := filepath.Abs(devTopDir)
	if err != nil {
		return ""
	}
	for _, dir := range []string{filepath.Dir(abs), abs} {
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			return dir
		}
	}
	return ""
}

// gitBranch returns the current branch of a git root, or "".
func gitBranch(root string) string {
	out, err := execGit(root, "branch", "--show-current")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// gitDirtyCount returns the count of uncommitted changes in the git root, or 0
// when git is unavailable.
func gitDirtyCount(root string) int {
	out, err := execGit(root, "status", "--porcelain")
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// Config parses the repo's config.yml lazily, falling back to the bundled
// default. A repo without .devtop gets the default config (ready to scaffold).
func (r *Repo) Config() (EngineConfig, error) {
	r.cfgOnce.Do(func() {
		r.cfg, r.cfgErr = loadEngineConfigFrom(r.paths.DevTop)
	})
	return r.cfg, r.cfgErr
}

// docsPathsFor resolves the doc store directory for a repo from its engine
// config: the `documentation` kind's declared path, falling back to the
// legacy .devtop/docs directory when the config predates the kind model.
func docsPathsFor(r *Repo) RepoPaths {
	p := r.paths
	if cfg, err := r.Config(); err == nil {
		if kind, ok := cfg.ArtifactKinds["documentation"]; ok && kind.Path != "" {
			p.Docs = filepath.Join(p.DevTop, kind.Path)
		}
	}
	return p
}

// loadEngineConfigFrom parses <devTop>/config.yml, falling back to the bundled
// default. Shared by LoadEngineConfig (legacy global) and per-repo Config.
func loadEngineConfigFrom(devTop string) (EngineConfig, error) {
	path := filepath.Join(devTop, "config.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			data = defaultEngineConfig
		} else {
			return EngineConfig{}, err
		}
	}
	var cfg EngineConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return EngineConfig{}, fmt.Errorf("invalid engine config %s: %w", path, err)
	}
	if cfg.ArtifactKinds == nil {
		cfg.ArtifactKinds = map[string]ArtifactKind{}
	}
	for name, kind := range cfg.ArtifactKinds {
		if kind.Schema != nil {
			if j, ok := toJSONable(kind.Schema).(map[string]interface{}); ok {
				kind.Schema = j
			}
		}
		cfg.ArtifactKinds[name] = kind
	}
	return cfg, nil
}

// Status computes the UI-facing status of the repo.
func (r *Repo) Status() RepoStatus {
	st := RepoStatus{
		Name:   r.Name,
		Path:   r.Root,
		Status: "ready",
	}
	if r.Initialized() {
		st.Initialized = true
	}
	// Uninitialized wins over everything: without .devtop/config.yml there is
	// nothing to serve, even inside a git repo.
	if !st.Initialized {
		st.Status = "uninit"
	} else if root := gitRootFrom(r.paths.DevTop); root != "" {
		st.HasGit = true
		st.Branch = gitBranch(root)
		if n := gitDirtyCount(root); n > 0 {
			st.Dirty = n
			st.Status = "dirty"
		}
	} else {
		st.Status = "nogit"
	}
	if entries, err := os.ReadDir(docsPathsFor(r).Docs); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".mdx") {
				st.Docs++
			}
		}
	}
	if _, cfgErr := r.Config(); cfgErr == nil {
		if resp, present := buildPipelineFor(r); present {
			for _, it := range resp.Items {
				if !it.Ready && len(it.Stages["tickets"]) == 0 {
					st.Pending++
				}
			}
		}
	}
	return st
}

// Registry is the set of repos served by this instance. In single-repo mode
// (DEVTOP_REPOS unset and no registry file) it holds exactly one entry backed
// by the globals.
type Registry struct {
	mu     sync.RWMutex
	repos  []*Repo
	byName map[string]*Repo
}

var registry = &Registry{byName: map[string]*Repo{}}

// registryFilePath is where the instance persists UI-registered repos. It is
// instance state, never repo state: repos don't know which instance serves
// them, and editing the file (or DEVTOP_REPOS, which is merged at startup) is
// the supported way to manage the registry outside the UI.
//
// In the container, the mounted config volume (/etc/devtop) holds the AI key;
// the registry goes next to it so registered repos survive container
// restarts. DEVTOP_CONFIG_DIR overrides the volume path; local runs fall back
// to the user config dir.
func registryFilePath() string {
	if p := strings.TrimSpace(os.Getenv("DEVTOP_REPOS_FILE")); p != "" {
		return p
	}
	for _, dir := range []string{strings.TrimSpace(os.Getenv("DEVTOP_CONFIG_DIR")), "/etc/devtop"} {
		if dir == "" {
			continue
		}
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return filepath.Join(dir, "repos.json")
		}
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "devtop", "repos.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "repos.json"
	}
	return filepath.Join(home, ".config", "devtop", "repos.json")
}

// loadRegistryFile reads the persisted repo paths. A missing or malformed
// file is an empty list, never an error.
func loadRegistryFile() []string {
	data, err := os.ReadFile(registryFilePath())
	if err != nil {
		return nil
	}
	var paths []string
	if err := json.Unmarshal(data, &paths); err != nil {
		return nil
	}
	return paths
}

// saveRegistryFile atomically persists the registered roots. The caller must
// not hold the registry lock (the snapshot is taken here).
func saveRegistryFile() error {
	registry.mu.RLock()
	paths := make([]string, 0, len(registry.repos))
	for _, r := range registry.repos {
		paths = append(paths, r.Root)
	}
	registry.mu.RUnlock()
	data, err := json.MarshalIndent(paths, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(registryFilePath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "repos-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, registryFilePath())
}

// initRegistry builds the registry from DEVTOP_REPOS (path-list of project
// roots) merged with the persisted registry file. It must run after the
// devtop root is resolved. No DEVTOP_REPOS and no registry file means zero
// projects — the UI shows the Add-project state, never a synthesized repo.
func initRegistry() error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.repos = nil
	registry.byName = map[string]*Repo{}

	raw := strings.TrimSpace(os.Getenv("DEVTOP_REPOS"))
	roots := loadRegistryFile()

	seen := map[string]bool{}
	all := append(filepath.SplitList(raw), roots...)
	for _, root := range all {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		// A configured or persisted root that no longer resolves (a stale
		// registry entry after a mount change, a deleted project) must not
		// fabricate a phantom "uninitialized" repo the UI cannot escape.
		// Skip it; the registry file self-cleans on the next add/remove.
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			fmt.Printf("Warning: skipping repo root %s: not a directory\n", abs)
			continue
		}
		registry.addLocked(newRepo(abs))
	}
	return nil
}

// repoNameForRoot derives a repo name from a devtop dir. The standard layout
// is <project>/.devtop, so the name comes from the project basename.
func repoNameForRoot(devTopDir string) string {
	abs, _ := filepath.Abs(devTopDir)
	base := filepath.Base(abs)
	if base == ".devtop" || strings.HasSuffix(base, ".devtop") {
		return filepath.Base(filepath.Dir(abs))
	}
	return base
}

func newRepo(root string) *Repo {
	dir := filepath.Join(root, ".devtop")
	return &Repo{
		Name:  filepath.Base(root),
		Root:  root,
		Dir:   dir,
		paths: newRepoPaths(dir),
	}
}

func (reg *Registry) addLocked(r *Repo) {
	reg.repos = append(reg.repos, r)
	reg.byName[r.Name] = r
}

// Resolve returns the repo named by name, or the first registered repo for "".
// Zero registered repos is an error: there is no synthetic single-repo
// fallback, the UI must offer the Add-project state instead.
func (reg *Registry) Resolve(name string) (*Repo, error) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	if name == "" || name == "__default__" {
		if len(reg.repos) == 0 {
			return nil, fmt.Errorf("no repos registered")
		}
		return reg.repos[0], nil
	}
	r, ok := reg.byName[name]
	if !ok {
		return nil, fmt.Errorf("unknown repo %q", name)
	}
	return r, nil
}

// List returns the repos in registration order. Duplicate names are folded.
func (reg *Registry) List() []*Repo {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	out := make([]*Repo, len(reg.repos))
	copy(out, reg.repos)
	return out
}

// Add registers a project root. The repo appears with its own status; a
// duplicate path or name is an error. The registry is persisted to disk so
// UI registrations survive restarts.
func (reg *Registry) Add(root string) (*Repo, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", root)
	}
	r := newRepo(abs)
	reg.mu.Lock()
	if existing, ok := reg.byName[r.Name]; ok {
		reg.mu.Unlock()
		return existing, fmt.Errorf("repo %q already registered (%s)", r.Name, existing.Root)
	}
	for _, e := range reg.repos {
		if e.Root == abs {
			reg.mu.Unlock()
			return e, fmt.Errorf("repo %s already registered", abs)
		}
	}
	reg.addLocked(r)
	reg.mu.Unlock()
	if err := saveRegistryFile(); err != nil {
		return r, fmt.Errorf("registered, but could not persist the registry: %w", err)
	}
	return r, nil
}

// Remove unregisters a repo by name. The last repo may be removed: an empty
// registry is a valid state, and the UI's Add-project flow re-registers.
func (reg *Registry) Remove(name string) (*Repo, error) {
	reg.mu.Lock()
	r, ok := reg.byName[name]
	if !ok {
		reg.mu.Unlock()
		return nil, fmt.Errorf("unknown repo %q", name)
	}
	out := make([]*Repo, 0, len(reg.repos)-1)
	for _, e := range reg.repos {
		if e != r {
			out = append(out, e)
		}
	}
	reg.repos = out
	delete(reg.byName, name)
	reg.mu.Unlock()
	if err := saveRegistryFile(); err != nil {
		return r, fmt.Errorf("removed, but could not persist the registry: %w", err)
	}
	return r, nil
}

// handleAPIRepoDelete unregisters a repo: DELETE /api/repos/{name}.
func handleAPIRepoDelete(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "repo name is required")
		return
	}
	removed, err := registry.Remove(name)
	if err != nil {
		status := http.StatusNotFound
		if strings.Contains(err.Error(), "last registered") {
			status = http.StatusConflict
		}
		writeJSONError(w, status, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"removed": removed.Name})
}

// Initialized reports whether the repo's .devtop has been scaffolded, i.e.
// whether it holds real content: a config.yml, the agents directory, or
// seeded docs. The scaffold materializes all three; a legacy repo may carry
// only docs/ and agents/. A bare folder (or a stale shell of storage dirs
// with no docs and no agents) is not initialized: nothing is written to its
// workspace until init scaffolds it.
//
// Deliberately NOT the parent git checkout: a git repo without .devtop must
// still be gated until init runs.
func (r *Repo) Initialized() bool {
	p := r.paths.DevTop
	if _, err := os.Stat(filepath.Join(p, "config.yml")); err == nil {
		return true
	}
	for _, sub := range []string{filepath.Join(p, "agents"), filepath.Join(p, "docs")} {
		if entries, err := os.ReadDir(sub); err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}

// Init scaffolds the repo's .devtop directory from the embedded templates:
// storage dirs, config.yml, config-declared kind dirs, the default agents and
// skills, and a welcome doc. It is the UI's "Initialize" action for an
// uninitialized repo, and the only write point — the runtime reads .devtop/
// afterwards. Non-destructive: existing files are never overwritten.
func (r *Repo) Init() error {
	return scaffoldRepo(r.paths)
}

// repoFromRequest resolves the active repo for a request: the `repo` query
// param or the X-Devtop-Repo header; empty means the registry default.
func repoFromRequest(w http.ResponseWriter, r *http.Request) (*Repo, bool) {
	name := r.URL.Query().Get("repo")
	if name == "" {
		name = r.Header.Get("X-Devtop-Repo")
	}
	repo, err := registry.Resolve(name)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return nil, false
	}
	return repo, true
}

// zeroRepoInstance reports whether this instance has no registered projects.
// These guards replace the classic single-repo boot: nothing is created for a
// project until it is added and initialized.
func zeroRepoInstance() bool {
	return len(registry.List()) == 0
}

// handleAPIRepos serves the registry: repo list (with status) via GET, and
// repo registration via POST {path}.
func handleAPIRepos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		repos := registry.List()
		out := make([]RepoStatus, 0, len(repos))
		for _, repo := range repos {
			out = append(out, repo.Status())
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	case http.MethodPost:
		var payload struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, "Invalid body")
			return
		}
		repo, err := registry.Add(payload.Path)
		if err != nil {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(repo.Status())
	default:
		http.Error(w, "Method not allowed", 405)
	}
}

// handleAPIRepoDetail serves one repo's paths and status. The chat runtime
// uses it to scope its tools to the active repo's filesystem.
func handleAPIRepoDetail(w http.ResponseWriter, r *http.Request) {
	repo, err := registry.Resolve(r.PathValue("name"))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	status := repo.Status()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"name":     repo.Name,
		"root":     repo.Root,
		"git_root": gitRootFrom(repo.paths.DevTop),
		"status":   status.Status,
		"devtop":   repo.paths.DevTop,
		"docs":     repo.paths.Docs,
		"tickets":  repo.paths.Tickets,
		"threads":  repo.paths.Threads,
	})
}

// writeJSONError responds with a JSON {"error": ...} body so the frontend can
// surface the message verbatim (e.g. duplicate repo registration).
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleAPIRepoInit scaffolds .devtop for the named repo.
func handleAPIRepoInit(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	if err := repo.Init(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(repo.Status())
}

// browseSeed returns the default folder-browser root. The workspace root (the
// directory that holds .devtop) is where the user's repos live — in a
// container that is the mounted volume, not the empty OS home. Fall back to
// the home directory, then the filesystem root.
func browseSeed() string {
	// DEVTOP_DIR is the root that contains the user's project dirs, so the
	// Add-repo browser starts there (the old single-repo layout seeded the
	// parent of a .devtop dir; root + registered projects seeds the root).
	if fi, err := os.Stat(DEVTOP_DIR); err == nil && fi.IsDir() {
		return DEVTOP_DIR
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return string(filepath.Separator)
}

// handleAPIFSList lists a directory for the Add-repo folder browser. The
// server owns the filesystem; the browser only renders what it is told.
// Hidden entries are skipped unless the path itself is hidden.
func handleAPIFSList(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = browseSeed()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		http.Error(w, "Invalid path", 400)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		http.Error(w, "Path not found", 404)
		return
	}
	if !fi.IsDir() {
		http.Error(w, "Not a directory", 400)
		return
	}

	type entry struct {
		Name       string `json:"name"`
		Dir        bool   `json:"dir"`
		HasGit     bool   `json:"has_git"`
		HasDevTop  bool   `json:"has_devtop"`
		HasSubDirs bool   `json:"has_subdirs"`
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	base := filepath.Base(abs)
	if base == "" {
		base = string(filepath.Separator)
	}
	out := []entry{}
	for _, e := range entries {
		// Follow symlinks-to-directories: home-shortcut project dirs are
		// symlinks, and they must be browsable. Files stay excluded.
		fi, err := os.Stat(filepath.Join(abs, e.Name()))
		if err != nil || !fi.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		hasGit := false
		if _, err := os.Stat(filepath.Join(abs, e.Name(), ".git")); err == nil {
			hasGit = true
		}
		hasDevTop := false
		if fi, err := os.Stat(filepath.Join(abs, e.Name(), ".devtop")); err == nil && fi.IsDir() {
			hasDevTop = true
		}
		// Cheap subdir probe: a directory is expandable when it has any
		// subdirectory (checked up to a bound so giant dirs stay fast).
		hasSubDirs := false
		if sub, err := os.ReadDir(filepath.Join(abs, e.Name())); err == nil {
			for i, s := range sub {
				if s.IsDir() && !strings.HasPrefix(s.Name(), ".") {
					hasSubDirs = true
					break
				}
				if i >= 99 {
					break
				}
			}
		}
		out = append(out, entry{Name: e.Name(), Dir: true, HasGit: hasGit, HasDevTop: hasDevTop, HasSubDirs: hasSubDirs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"path":    abs,
		"name":    base,
		"entries": out,
	})
}
