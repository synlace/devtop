package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// newRepoTemp creates a temp project root with a git repo (when gitInit) and
// writes a .devtop/config.yml (when initDevtop).
func newRepoTemp(t *testing.T, name string, initDevtop bool) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if initDevtop {
		r := newRepo(root)
		if err := r.Init(); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// cleanRegistry empties the shared registry (with cleanup) without touching
// the environment; used by tests that exercise registry.Add directly. Every
// helper pins the registry file into a temp dir so tests never write the
// real user config.
func cleanRegistry(t *testing.T) {
	t.Helper()
	t.Setenv("DEVTOP_REPOS_FILE", filepath.Join(t.TempDir(), "repos.json"))
	t.Cleanup(func() {
		registry.mu.Lock()
		registry.repos = nil
		registry.byName = map[string]*Repo{}
		registry.mu.Unlock()
	})
}

// withRegistryEnv sets DEVTOP_REPOS and re-inits the registry, restoring the
// previous state after the test.
func withRegistryEnv(t *testing.T, roots ...string) {
	t.Helper()
	cleanRegistry(t)
	t.Setenv("DEVTOP_REPOS", stringsJoin(roots, string(os.PathListSeparator)))
	if err := initRegistry(); err != nil {
		t.Fatal(err)
	}
}

func stringsJoin(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func TestRegistryResolve_DefaultSynthetic(t *testing.T) {
	// Empty registry resolves to a synthetic repo built from the globals.
	repo, err := registry.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if repo.paths.DevTop != DEVTOP_DIR {
		t.Fatalf("expected devtop %s, got %s", DEVTOP_DIR, repo.paths.DevTop)
	}
	if !repo.Single {
		t.Fatal("expected single-repo fallback")
	}
}

func TestRegistry_MultiRepoResolveAndIsolation(t *testing.T) {
	rootA := newRepoTemp(t, "alpha", true)
	rootB := newRepoTemp(t, "beta", true)
	withRegistryEnv(t, rootA, rootB)

	a, err := registry.Resolve("alpha")
	if err != nil {
		t.Fatal(err)
	}
	b, err := registry.Resolve("beta")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("expected distinct repos")
	}
	if _, err := registry.Resolve("nope"); err == nil {
		t.Fatal("expected unknown repo error")
	}

	// Default resolution picks the first registered repo.
	def, err := registry.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "alpha" {
		t.Fatalf("expected default alpha, got %s", def.Name)
	}

	// Write a doc in each repo; scoped reads must not cross.
	if err := writeDocToFileSystemP(a.paths, "alpha-doc", "---\ntitle: Alpha\n---\nbody a"); err != nil {
		t.Fatal(err)
	}
	if err := writeDocToFileSystemP(b.paths, "beta-doc", "---\ntitle: Beta\n---\nbody b"); err != nil {
		t.Fatal(err)
	}
	da, _ := listDocsP(a.paths)
	db, _ := listDocsP(b.paths)
	found := func(docs []DocSlug, want string) bool {
		for _, d := range docs {
			if d.Slug == want {
				return true
			}
		}
		return false
	}
	if !found(da, "alpha-doc") || found(da, "beta-doc") {
		t.Fatalf("repo A docs leaked: %+v", da)
	}
	if !found(db, "beta-doc") || found(db, "alpha-doc") {
		t.Fatalf("repo B docs leaked: %+v", db)
	}
}

func TestRegistry_AddDedupes(t *testing.T) {
	root := newRepoTemp(t, "gamma", false)
	withRegistryEnv(t)

	r, err := registry.Add(root)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "gamma" {
		t.Fatalf("expected gamma, got %s", r.Name)
	}
	if _, err := registry.Add(root); err == nil {
		t.Fatal("expected duplicate path error")
	}
	if got, _ := registry.Resolve("gamma"); got == nil {
		t.Fatal("expected gamma resolved")
	}
}

func TestRepoStatus_UninitNogitReady(t *testing.T) {
	plain := newRepoTemp(t, "plain", false)
	withRegistryEnv(t)
	r := newRepo(plain)
	st := r.Status()
	if st.Status != "uninit" || st.Initialized {
		t.Fatalf("expected uninit, got %s initialized=%v", st.Status, st.Initialized)
	}

	// Init scaffolds, but without a git repo the status is nogit.
	if err := r.Init(); err != nil {
		t.Fatal(err)
	}
	st = r.Status()
	if st.Status != "nogit" || !st.Initialized {
		t.Fatalf("expected nogit initialized, got %s initialized=%v", st.Status, st.Initialized)
	}
	if _, err := os.Stat(filepath.Join(r.paths.DevTop, "config.yml")); err != nil {
		t.Fatal("config.yml not materialized by Init")
	}
	if _, err := os.Stat(filepath.Join(r.paths.Docs, "index.mdx")); err != nil {
		t.Fatal("welcome doc not materialized by Init")
	}

	// Inside a git repo the status is ready once the scaffold is committed.
	gitRoot := newRepoTemp(t, "gitrepo", false)
	if _, err := execGit(gitRoot, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	gr := newRepo(gitRoot)
	if err := gr.Init(); err != nil {
		t.Fatal(err)
	}
	// The Init scaffold is uncommitted, so the repo is dirty first.
	if st := gr.Status(); st.Status != "dirty" {
		t.Fatalf("expected dirty before commit, got %s", st.Status)
	}
	if _, err := execGit(gitRoot, "-c", "user.email=t@t", "-c", "user.name=t", "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := execGit(gitRoot, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "scaffold", "--allow-empty"); err != nil {
		t.Fatal(err)
	}
	if st := gr.Status(); st.Status != "ready" || st.Branch == "" {
		t.Fatalf("expected ready with branch, got %s branch=%q", st.Status, st.Branch)
	}
}

func TestHandler_RepoScopedDocs(t *testing.T) {
	rootA := newRepoTemp(t, "scoped-a", true)
	rootB := newRepoTemp(t, "scoped-b", true)
	withRegistryEnv(t, rootA, rootB)

	ra, _ := registry.Resolve("scoped-a")
	rb, _ := registry.Resolve("scoped-b")
	if err := writeDocToFileSystemP(ra.paths, "only-a", "---\ntitle: A\n---\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeDocToFileSystemP(rb.paths, "only-b", "---\ntitle: B\n---\n"); err != nil {
		t.Fatal(err)
	}

	get := func(repo string) []DocSlug {
		req := httptest.NewRequest("GET", "/api/docs", nil)
		if repo != "" {
			req = httptest.NewRequest("GET", "/api/docs?repo="+repo, nil)
		}
		rr := httptest.NewRecorder()
		handleAPIDocs(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		var out []DocSlug
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	has := func(docs []DocSlug, slug string) bool {
		for _, d := range docs {
			if d.Slug == slug {
				return true
			}
		}
		return false
	}

	if docs := get("scoped-a"); !has(docs, "only-a") || has(docs, "only-b") {
		t.Fatalf("scoped-a returned %+v", docs)
	}
	if docs := get("scoped-b"); !has(docs, "only-b") || has(docs, "only-a") {
		t.Fatalf("scoped-b returned %+v", docs)
	}
}

func TestHandler_UnknownRepo404(t *testing.T) {
	withRegistryEnv(t, newRepoTemp(t, "known", true))
	req := httptest.NewRequest("GET", "/api/docs?repo=missing", nil)
	rr := httptest.NewRecorder()
	handleAPIDocs(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestHandler_AddAndInitRepo(t *testing.T) {
	cleanRegistry(t)
	root := newRepoTemp(t, "added", false)

	// POST /api/repos registers it.
	body, _ := json.Marshal(map[string]string{"path": root})
	req := httptest.NewRequest("POST", "/api/repos", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	handleAPIRepos(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("add status %d: %s", rr.Code, rr.Body.String())
	}
	var st RepoStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "uninit" {
		t.Fatalf("expected uninit, got %s", st.Status)
	}

	// GET /api/repos lists it.
	req = httptest.NewRequest("GET", "/api/repos", nil)
	rr = httptest.NewRecorder()
	handleAPIRepos(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status %d", rr.Code)
	}
	var list []RepoStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "added" {
		t.Fatalf("unexpected list: %+v", list)
	}

	// POST /api/repos/init scaffolds it.
	req = httptest.NewRequest("POST", "/api/repos/init?repo=added", nil)
	rr = httptest.NewRecorder()
	handleAPIRepoInit(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("init status %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".devtop", "config.yml")); err != nil {
		t.Fatal("init did not materialize config.yml")
	}
}

func TestHandler_FSList(t *testing.T) {
	root := newRepoTemp(t, "fsroot", false)
	gitChild := filepath.Join(root, "git-repo")
	if err := os.MkdirAll(gitChild, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := execGit(gitChild, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "plain-dir"), 0755); err != nil {
		t.Fatal(err)
	}
	// A hidden dir is skipped when the parent is not hidden.
	if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0755); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/fs/list?path="+root, nil)
	rr := httptest.NewRecorder()
	handleAPIFSList(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Path    string `json:"path"`
		Entries []struct {
			Name   string `json:"name"`
			Dir    bool   `json:"dir"`
			HasGit bool   `json:"has_git"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range resp.Entries {
		seen[e.Name] = true
		if e.Name == "git-repo" && !e.HasGit {
			t.Fatal("git-repo should have has_git=true")
		}
	}
	if !seen["plain-dir"] {
		t.Fatal("plain-dir missing")
	}
	if seen[".hidden"] {
		t.Fatal("hidden dir should be skipped")
	}

	// A file is rejected.
	f := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", "/api/fs/list?path="+f, nil)
	rr = httptest.NewRecorder()
	handleAPIFSList(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for file, got %d", rr.Code)
	}
}

func TestRegistryFile_PersistsAcrossRestart(t *testing.T) {
	cleanRegistry(t)
	rootA := newRepoTemp(t, "persist-a", false)
	rootB := newRepoTemp(t, "persist-b", false)

	// Point the registry file into a temp location so tests never touch
	// the real user config.
	file := filepath.Join(t.TempDir(), "repos.json")
	t.Setenv("DEVTOP_REPOS_FILE", file)

	if _, err := registry.Add(rootA); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Add(rootB); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("registry file not written: %v", err)
	}
	var paths []string
	if err := json.Unmarshal(data, &paths); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 persisted paths, got %v", paths)
	}

	// "Restart": re-init from the file alone (no env).
	t.Setenv("DEVTOP_REPOS", "")
	if err := initRegistry(); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("persist-a"); err != nil {
		t.Fatal("persist-a missing after re-init")
	}
	if _, err := registry.Resolve("persist-b"); err != nil {
		t.Fatal("persist-b missing after re-init")
	}
}

func TestRegistry_RemoveAndLastRepoGuard(t *testing.T) {
	cleanRegistry(t)
	file := filepath.Join(t.TempDir(), "repos.json")
	t.Setenv("DEVTOP_REPOS_FILE", file)

	if _, err := registry.Add(newRepoTemp(t, "rem-a", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Add(newRepoTemp(t, "rem-b", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Remove("rem-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve("rem-a"); err == nil {
		t.Fatal("rem-a should be gone")
	}
	if _, err := registry.Resolve("rem-b"); err != nil {
		t.Fatal("rem-b should remain")
	}
	// Removing the last repo is refused so the UI can never get locked out.
	if _, err := registry.Remove("rem-b"); err == nil {
		t.Fatal("expected last-repo guard error")
	}
	data, _ := os.ReadFile(file)
	if string(data) == "" {
		t.Fatal("registry file empty after remove")
	}
	var paths []string
	json.Unmarshal(data, &paths)
	if len(paths) != 1 || paths[0] == "" {
		t.Fatalf("unexpected persisted paths: %v", paths)
	}
}

func TestHandler_DeleteRepo(t *testing.T) {
	cleanRegistry(t)
	file := filepath.Join(t.TempDir(), "repos.json")
	t.Setenv("DEVTOP_REPOS_FILE", file)
	if _, err := registry.Add(newRepoTemp(t, "del-a", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Add(newRepoTemp(t, "del-b", false)); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("DELETE", "/api/repos/del-a", nil)
	req.SetPathValue("name", "del-a")
	rr := httptest.NewRecorder()
	handleAPIRepoDelete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := registry.Resolve("del-a"); err == nil {
		t.Fatal("del-a should be gone")
	}

	// Unknown repo -> 404; last repo -> 409.
	req = httptest.NewRequest("DELETE", "/api/repos/nope", nil)
	req.SetPathValue("name", "nope")
	rr = httptest.NewRecorder()
	handleAPIRepoDelete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	req = httptest.NewRequest("DELETE", "/api/repos/del-b", nil)
	req.SetPathValue("name", "del-b")
	rr = httptest.NewRecorder()
	handleAPIRepoDelete(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for last repo, got %d", rr.Code)
	}
}

func TestPipeline_RepoScopedConfig(t *testing.T) {
	rootA := newRepoTemp(t, "pipe-a", true)
	rootB := newRepoTemp(t, "pipe-b", true)
	withRegistryEnv(t, rootA, rootB)

	ra, _ := registry.Resolve("pipe-a")
	rb, _ := registry.Resolve("pipe-b")

	// Repo A: doc with eligible prospect for prds -> pipeline has the doc.
	if err := writeDocToFileSystemP(ra.paths, "feat-a", "---\ntitle: Feat A\nderive_prospects:\n  prds: eligible\nprospect_by: user\n---\nbody\n"); err != nil {
		t.Fatal(err)
	}
	// Repo B: doc with not-eligible verdict.
	if err := writeDocToFileSystemP(rb.paths, "feat-b", "---\ntitle: Feat B\nderive_prospects:\n  prds: not-eligible\nprospect_by: user\n---\nbody\n"); err != nil {
		t.Fatal(err)
	}

	pa, _ := buildPipelineFor(ra)
	pb, _ := buildPipelineFor(rb)
	prospectOf := func(items []PipelineItem, slug string) string {
		for _, it := range items {
			if it.Slug == slug {
				return it.Prospect
			}
		}
		return ""
	}
	if got := prospectOf(pa.Items, "feat-a"); got != "eligible" {
		t.Fatalf("repo A prospect = %q", got)
	}
	if got := prospectOf(pb.Items, "feat-b"); got != "not-eligible" {
		t.Fatalf("repo B prospect = %q", got)
	}
	slugs := func(items []PipelineItem) map[string]bool {
		m := map[string]bool{}
		for _, it := range items {
			m[it.Slug] = true
		}
		return m
	}
	sa, sb := slugs(pa.Items), slugs(pb.Items)
	if !sa["feat-a"] || !sb["feat-b"] {
		t.Fatalf("docs missing: A=%v B=%v", sa, sb)
	}
	if sa["feat-b"] || sb["feat-a"] {
		t.Fatal("pipeline items leaked across repos")
	}
}

func TestHandler_FSList_DefaultSeed(t *testing.T) {
	oldDir := DEVTOP_DIR
	defer func() { DEVTOP_DIR = oldDir }()

	ws := t.TempDir()
	DEVTOP_DIR = filepath.Join(ws, ".devtop")
	seed := filepath.Join(ws, "seed-repo")
	if err := os.MkdirAll(seed, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := execGit(seed, "init", "-q"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/fs/list", nil)
	rr := httptest.NewRecorder()
	handleAPIFSList(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Path    string `json:"path"`
		Entries []struct {
			Name   string `json:"name"`
			HasGit bool   `json:"has_git"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Path != ws {
		t.Fatalf("seed path = %q, want %q", resp.Path, ws)
	}
	found := false
	for _, e := range resp.Entries {
		if e.Name == "seed-repo" && e.HasGit {
			found = true
		}
	}
	if !found {
		t.Fatalf("seed listing missing git dir: %s", rr.Body.String())
	}
}

func TestRegistryFilePathPreferConfigVolume(t *testing.T) {
	t.Setenv("DEVTOP_REPOS_FILE", "")
	dir := t.TempDir()
	t.Setenv("DEVTOP_CONFIG_DIR", dir)
	if got := registryFilePath(); got != filepath.Join(dir, "repos.json") {
		t.Fatalf("registryFilePath = %q, want %q", got, filepath.Join(dir, "repos.json"))
	}
}

func TestRegistryHasSynthetic(t *testing.T) {
	cleanRegistry(t)
	if registryHasSynthetic() {
		t.Fatal("empty registry reported synthetic")
	}
	real := filepath.Join(t.TempDir(), "real")
	if err := os.MkdirAll(real, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Add(real); err != nil {
		t.Fatal(err)
	}
	if registryHasSynthetic() {
		t.Fatal("real repo reported synthetic")
	}
	registry.mu.Lock()
	registry.repos = []*Repo{{Name: "synthetic", Single: true}}
	registry.byName = map[string]*Repo{"synthetic": {Name: "synthetic", Single: true}}
	registry.mu.Unlock()
	if !registryHasSynthetic() {
		t.Fatal("synthetic-only registry not reported")
	}
}

func TestInitRegistryFallsBackByWorkspaceShape(t *testing.T) {
	oldDir := DEVTOP_DIR
	defer func() { DEVTOP_DIR = oldDir }()

	// A plain folder mounts with zero repos: no synthetic fallback.
	plain := t.TempDir()
	DEVTOP_DIR = filepath.Join(plain, ".devtop")
	cleanRegistry(t)
	t.Setenv("DEVTOP_REPOS", "")
	if err := initRegistry(); err != nil {
		t.Fatal(err)
	}
	if got := registry.List(); len(got) != 0 {
		t.Fatalf("plain folder synthesized %d repos, want 0", len(got))
	}

	// A git checkout still gets the classic single-repo fallback.
	repoWorkspace := t.TempDir()
	if _, err := execGit(repoWorkspace, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	DEVTOP_DIR = filepath.Join(repoWorkspace, ".devtop")
	cleanRegistry(t)
	if err := initRegistry(); err != nil {
		t.Fatal(err)
	}
	list := registry.List()
	if len(list) != 1 || !list[0].Single {
		t.Fatalf("git workspace synthesized %d repos, want the single fallback", len(list))
	}
}

func TestZeroRepoInstanceWritesNothingToWorkspace(t *testing.T) {
	oldDir := DEVTOP_DIR
	defer func() { DEVTOP_DIR = oldDir }()

	// A plain, non-git folder of repos mounts with zero repos. The UI boot
	// path (models fetch, viewstate read/write, repo list) must not create
	// anything under the workspace until a repo is added.
	ws := t.TempDir()
	DEVTOP_DIR = filepath.Join(ws, ".devtop")
	cleanRegistry(t)
	t.Setenv("DEVTOP_REPOS", "")
	t.Setenv("AI_API_KEY", "")
	t.Setenv("AI_BASE_URL", "")

	if err := initRegistry(); err != nil {
		t.Fatal(err)
	}
	if got := registry.List(); len(got) != 0 {
		t.Fatalf("plain folder synthesized %d repos, want 0", len(got))
	}
	if !zeroRepoInstance() {
		t.Fatal("expected zero-repo instance")
	}

	// GET /api/models: no fetch, no cache write.
	req := httptest.NewRequest("GET", "/api/models", nil)
	rr := httptest.NewRecorder()
	handleAPIModels(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("models status %d: %s", rr.Code, rr.Body.String())
	}
	var modelsResp struct {
		Models []ModelInfo `json:"models"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &modelsResp); err != nil {
		t.Fatal(err)
	}
	if len(modelsResp.Models) != 0 {
		t.Fatalf("expected empty models on zero-repo boot, got %+v", modelsResp.Models)
	}

	// GET then PUT viewstate: in-memory only.
	req = httptest.NewRequest("GET", "/api/viewstate", nil)
	rr = httptest.NewRecorder()
	handleAPIGetViewState(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("viewstate get status %d: %s", rr.Code, rr.Body.String())
	}
	req = httptest.NewRequest("PUT", "/api/viewstate", bytes.NewReader([]byte(`{"selectedTab":"chat"}`)))
	rr = httptest.NewRecorder()
	handleAPIPutViewState(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("viewstate put status %d: %s", rr.Code, rr.Body.String())
	}

	// Nothing may exist under the workspace after the boot path.
	if _, err := os.Stat(DEVTOP_DIR); !os.IsNotExist(err) {
		t.Fatalf("zero-repo boot created %s", DEVTOP_DIR)
	}
	entries, err := os.ReadDir(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("workspace not left empty after boot: %v", entries)
	}

	// GET /api/repos returns the empty array, not null.
	req = httptest.NewRequest("GET", "/api/repos", nil)
	rr = httptest.NewRecorder()
	handleAPIRepos(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("repos status %d", rr.Code)
	}
	var list []RepoStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list == nil || len(list) != 0 {
		t.Fatalf("expected empty repo list, got %+v", list)
	}
}
