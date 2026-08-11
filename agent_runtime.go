package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Agent and skill model. Agents and skills are repo-owned artifacts under the
// kinds declared in config.yml (default .devtop/agents/ and .devtop/skills/),
// materialized from the bundled templates at init. The runtime reads only
// these files — there is no built-in fallback prompt or tool set.

// Agent runtime configuration (top-level `agent_runtime` in config.yml).
// Default names the agent the embedded chat agent uses; the agent file must
// exist in .devtop/agents/ — there is no fallback.

// AgentDef is the frontmatter of one .devtop/agents/<slug>.mdx file.
type AgentDef struct {
	Title       string      `yaml:"title" json:"title"`
	Model       string      `yaml:"model" json:"model"`
	Tools       []string    `yaml:"tools" json:"tools"`
	Skills      []string    `yaml:"skills" json:"skills"`
	Permissions Permissions `yaml:"permissions" json:"permissions"`
	Body        string      `json:"-"`
}

// Permissions are glob scopes relative to the workspace's .devtop/ config,
// e.g. "docs/**" or "tickets/**". An empty write list denies writes.
type Permissions struct {
	Read  []string `yaml:"read" json:"read"`
	Write []string `yaml:"write" json:"write"`
}

// SkillDef is the frontmatter of one .devtop/skills/<slug>.mdx file.
type SkillDef struct {
	Title       string   `yaml:"title" json:"title"`
	Description string   `yaml:"description" json:"description"`
	Triggers    []string `yaml:"triggers" json:"triggers"`
	Body        string   `json:"-"`
}

// toolGroups expand a group name declared in an agent's `tools` list.
var toolGroups = map[string][]string{
	"base":      {"git_commit", "ask_user", "list_docs"},
	"docs":      {"read_doc", "read_doc_at", "list_doc_revisions", "write_doc"},
	"tickets":   {"list_tickets", "read_ticket", "create_ticket", "update_ticket", "add_comment"},
	"workspace": {"read_workspace_file", "list_workspace_files"},
}

type agentEntry struct {
	Slug string
	Def  AgentDef
}

type skillEntry struct {
	Slug string
	Def  SkillDef
}

// agentRuntime is the resolved execution context of one agent: its allowlist,
// permission scopes, bound skills, and built system prompt.
type agentRuntime struct {
	Slug        string
	Def         AgentDef
	allowlist   map[string]bool // nil means all registered tools allowed
	writeScopes []string
	readScopes  []string
	skills      []SkillDef
	prompt      string
}

func kindDir(name string) string {
	if k, ok := engineConfig.ArtifactKinds[name]; ok && k.Path != "" {
		return filepath.Join(DEVTOP_DIR, k.Path)
	}
	return ""
}

func kindDirFor(cfg EngineConfig, p RepoPaths, name string) string {
	if k, ok := cfg.ArtifactKinds[name]; ok && k.Path != "" {
		return filepath.Join(p.DevTop, k.Path)
	}
	return ""
}

func agentsDir() string { return kindDir("agents") }
func skillsDir() string { return kindDir("skills") }

func matchSlug(slug, want string) bool {
	return strings.ToLower(strings.TrimSuffix(slug, ".mdx")) ==
		strings.ToLower(strings.TrimSuffix(want, ".mdx"))
}

func listAgentDefs() ([]agentEntry, error) {
	return listAgentDefsIn(defaultPaths())
}

func listAgentDefsIn(p RepoPaths) ([]agentEntry, error) {
	entries, err := listDefs(kindDirFor(engineConfig, p, "agents"), func() interface{} { return &AgentDef{} })
	if err != nil {
		return nil, err
	}
	out := make([]agentEntry, 0, len(entries))
	for _, e := range entries {
		def := *e.Def.(*AgentDef)
		def.Body = e.Body
		out = append(out, agentEntry{Slug: e.Slug, Def: def})
	}
	return out, nil
}

func listSkillDefs() ([]skillEntry, error) {
	return listSkillDefsIn(defaultPaths())
}

func listSkillDefsIn(p RepoPaths) ([]skillEntry, error) {
	entries, err := listDefs(kindDirFor(engineConfig, p, "skills"), func() interface{} { return &SkillDef{} })
	if err != nil {
		return nil, err
	}
	out := make([]skillEntry, 0, len(entries))
	for _, e := range entries {
		def := *e.Def.(*SkillDef)
		def.Body = e.Body
		out = append(out, skillEntry{Slug: e.Slug, Def: def})
	}
	return out, nil
}

type rawDefEntry struct {
	Slug string
	Def  interface{}
	Body string
}

// listDefs walks a kind dir and parses frontmatter into the given struct
// pointer via newMeta. A fresh meta is allocated per file: parsers do not
// zero fields absent from the frontmatter, so reuse would leak stale values.
func listDefs(dir string, newMeta func() interface{}) ([]rawDefEntry, error) {
	if dir == "" {
		return nil, nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	out := []rawDefEntry{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".mdx") {
			return nil
		}
		meta := newMeta()
		body, parseErr := parseFrontmatterFile(path, meta)
		if parseErr != nil {
			return nil
		}
		out = append(out, rawDefEntry{
			Slug: strings.TrimSuffix(filepath.Base(path), ".mdx"),
			Def:  meta,
			Body: strings.TrimSpace(string(body)),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func findSkillDef(slug string) (SkillDef, bool, error) {
	skills, err := listSkillDefs()
	if err != nil {
		return SkillDef{}, false, err
	}
	for _, s := range skills {
		if matchSlug(s.Slug, slug) {
			return s.Def, true, nil
		}
	}
	return SkillDef{}, false, nil
}

// buildAgentRuntime resolves a repo-owned agent definition to a runtime.
func buildAgentRuntime(slug string) (*agentRuntime, error) {
	return buildAgentRuntimeIn(engineConfig, defaultPaths(), slug)
}

// buildAgentRuntimeFor resolves a repo-owned agent for a specific repo; it is
// what handlers use so allowlists and scopes come from the owning repo.
func buildAgentRuntimeFor(repo *Repo, slug string) (*agentRuntime, error) {
	cfg, err := repo.Config()
	if err != nil {
		return nil, err
	}
	return buildAgentRuntimeIn(cfg, repo.paths, slug)
}

func buildAgentRuntimeIn(cfg EngineConfig, p RepoPaths, slug string) (*agentRuntime, error) {
	entries, err := listAgentDefsIn(p)
	if err != nil {
		return nil, err
	}
	var found *agentEntry
	for i := range entries {
		if matchSlug(entries[i].Slug, slug) {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("agent %q not found in %s", slug, kindDirFor(cfg, p, "agents"))
	}

	rt := &agentRuntime{
		Slug:        found.Slug,
		Def:         found.Def,
		writeScopes: found.Def.Permissions.Write,
		readScopes:  found.Def.Permissions.Read,
	}

	if len(found.Def.Tools) > 0 {
		rt.allowlist = map[string]bool{}
		for _, t := range found.Def.Tools {
			if group, ok := toolGroups[t]; ok {
				for _, name := range group {
					rt.allowlist[name] = true
				}
			} else {
				rt.allowlist[t] = true
			}
		}
	}

	for _, s := range found.Def.Skills {
		def, ok, err := findSkillDefIn(p, s)
		if err != nil || !ok {
			continue
		}
		rt.skills = append(rt.skills, def)
	}

	rt.prompt = buildAgentPrompt(rt)
	return rt, nil
}

func findSkillDefIn(p RepoPaths, slug string) (SkillDef, bool, error) {
	skills, err := listSkillDefsIn(p)
	if err != nil {
		return SkillDef{}, false, err
	}
	for _, s := range skills {
		if matchSlug(s.Slug, slug) {
			return s.Def, true, nil
		}
	}
	return SkillDef{}, false, nil
}

// activeRuntime resolves the configured default agent, or nil when it is not
// deployed (the .devtop/agents file is missing).
func activeRuntime() *agentRuntime {
	slug := strings.TrimSpace(engineConfig.AgentRuntime.Default)
	if slug == "" {
		return nil
	}
	rt, _ := buildAgentRuntime(slug)
	return rt
}

// activeRuntimeFor resolves the configured default agent of a repo.
func activeRuntimeFor(repo *Repo) *agentRuntime {
	if repo == nil {
		return activeRuntime()
	}
	cfg, err := repo.Config()
	if err != nil {
		return nil
	}
	slug := strings.TrimSpace(cfg.AgentRuntime.Default)
	if slug == "" {
		return nil
	}
	rt, _ := buildAgentRuntimeFor(repo, slug)
	return rt
}

// buildAgentPrompt composes the system prompt: the agent body, then the
// bodies of the bound skills. No global fallback text is prepended — the
// agent's own body carries its instructions.
func buildAgentPrompt(rt *agentRuntime) string {
	parts := []string{}
	if body := strings.TrimSpace(rt.Def.Body); body != "" {
		parts = append(parts, body)
	}
	for _, s := range rt.skills {
		if b := strings.TrimSpace(s.Body); b != "" {
			title := s.Title
			if title == "" {
				title = "Skill"
			}
			parts = append(parts, "## Skill: "+title+"\n\n"+b)
		}
	}
	return strings.Join(parts, "\n\n")
}

// resolveActivePrompt returns the configured default agent's prompt, or ""
// when no default agent is deployed. There is no fallback.
func resolveActivePrompt() string {
	if rt := activeRuntime(); rt != nil {
		return rt.prompt
	}
	return ""
}

// writeToolPathers map write tools to the DEVTOP-relative path they target.
// Permission enforcement depends on this mapping being complete.
var writeToolPathers = map[string]func(map[string]interface{}) (string, bool){
	"write_doc":      docToolPath,
	"write_artifact": artifactToolPath,
	"create_ticket":  ticketToolPath,
	"update_ticket":  ticketToolPath,
	"add_comment":    ticketToolPath,
}

var readToolPathers = map[string]func(map[string]interface{}) (string, bool){
	"read_doc":           docToolPath,
	"read_doc_at":        docToolPath,
	"list_doc_revisions": docToolPath,
	"read_ticket":        ticketToolPath,
}

// artifactToolPath mirrors write_artifact placement rules for any kind.
func artifactToolPath(args map[string]interface{}) (string, bool) {
	kind, _ := args["kind"].(string)
	id, _ := args["id"].(string)
	return resolveArtifactWriteTarget(kind, id)
}

// docToolPath mirrors writeDocToFileSystem's placement rules.
func docToolPath(args map[string]interface{}) (string, bool) {
	p, _ := args["path"].(string)
	p = strings.TrimSuffix(p, ".mdx")
	if p == "" || strings.Contains(p, "..") {
		return "", false
	}
	if strings.Contains(p, "/") || p == "index" {
		return "docs/" + p + ".mdx", true
	}
	return "docs/" + p + "/index.mdx", true
}

func ticketToolPath(args map[string]interface{}) (string, bool) {
	id, _ := args["id"].(string)
	if id == "" || filepath.Base(id) != id {
		return "", false
	}
	return "tickets/" + id + ".md", true
}

// authorizeTool returns "" when the tool call is permitted for the agent,
// or a human-readable denial the model receives as the tool result.
func (rt *agentRuntime) authorizeTool(name string, args map[string]interface{}) string {
	if rt.allowlist != nil && !rt.allowlist[name] {
		return fmt.Sprintf("Error: tool %q is not allowed for agent %q", name, rt.Slug)
	}
	if p, ok := writeToolPathers[name]; ok {
		rel, ok := p(args)
		if !ok {
			return fmt.Sprintf("Error: cannot determine target path for tool %q", name)
		}
		if !scopeMatches(rt.writeScopes, rel) {
			return fmt.Sprintf("Error: tool %q targets %q, outside the agent's write scope", name, rel)
		}
		return ""
	}
	if p, ok := readToolPathers[name]; ok && len(rt.readScopes) > 0 {
		if rel, ok := p(args); ok && !scopeMatches(rt.readScopes, rel) {
			return fmt.Sprintf("Error: tool %q targets %q, outside the agent's read scope", name, rel)
		}
	}
	return ""
}

func scopeMatches(scopes []string, rel string) bool {
	for _, s := range scopes {
		if matchGlob(s, rel) {
			return true
		}
	}
	return false
}

// matchGlob matches a slash-separated glob where "**" spans any number of
// segments and "*" matches one segment (via filepath.Match).
func matchGlob(pattern, name string) bool {
	return matchGlobSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchGlobSegs(p, n []string) bool {
	for len(p) > 0 {
		switch p[0] {
		case "**":
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(n); i++ {
				if matchGlobSegs(p[1:], n[i:]) {
					return true
				}
			}
			return false
		case "*":
			if len(n) == 0 {
				return false
			}
			p, n = p[1:], n[1:]
		default:
			if len(n) == 0 {
				return false
			}
			if ok, err := filepath.Match(p[0], n[0]); err != nil || !ok {
				return false
			}
			p, n = p[1:], n[1:]
		}
	}
	return len(n) == 0
}

// dispatchToolResolved runs a tool under an agent runtime's allowlist and
// permission scopes (legacy global scope). A nil runtime skips all checks.
func dispatchToolResolved(rt *agentRuntime, name string, args map[string]interface{}) string {
	return dispatchToolForRepo(nil, rt, name, args)
}
