package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

// The opinionated default config, bundled into the binary. A repo can override
// it by committing its own $DEVTOP_DIR/config.yml (materialized on first run).
//
//go:embed configs/default.yml
var defaultEngineConfig []byte

type EngineNav struct {
	Label string `yaml:"label" json:"label"`
	Icon  string `yaml:"icon" json:"icon"`
	Order int    `yaml:"order" json:"order"`
	View  string `yaml:"view" json:"view"`
}

type ArtifactKind struct {
	Path             string                 `yaml:"path" json:"path"`
	Extension        string                 `yaml:"extension" json:"extension"`
	AgentWritable    bool                   `yaml:"agent_writable" json:"agent_writable"`
	View             string                 `yaml:"view" json:"view"`
	Nav              *EngineNav             `yaml:"nav" json:"nav,omitempty"`
	RequiresApproval bool                   `yaml:"requires_approval" json:"requires_approval"`
	Schema           map[string]interface{} `yaml:"schema" json:"schema,omitempty"`
}

type DerivationEdge struct {
	From      string `yaml:"from" json:"from"`
	To        string `yaml:"to" json:"to"`
	Transform string `yaml:"transform" json:"transform"`
	Gate      string `yaml:"gate" json:"gate,omitempty"`
	Agent     string `yaml:"agent" json:"agent,omitempty"`
	// Classifier names the agent that suggests whether a source artifact
	// qualifies for this edge (derive_prospects in source frontmatter).
	// When bound, derivation requires an `eligible` verdict for this edge.
	Classifier string `yaml:"classifier" json:"classifier,omitempty"`
}

type ReplanPhases struct {
	BeforeApproval       string `yaml:"before_approval" json:"before_approval"`
	AfterApprovalPending string `yaml:"after_approval_pending" json:"after_approval_pending"`
	InFlight             string `yaml:"in_flight" json:"in_flight"`
	Shipped              string `yaml:"shipped" json:"shipped"`
}

type Replan struct {
	Detect     string       `yaml:"detect" json:"detect"`
	StaleBadge bool         `yaml:"stale_badge" json:"stale_badge"`
	Phases     ReplanPhases `yaml:"phases" json:"phases"`
}

type PromptDef struct {
	Trigger string `yaml:"trigger" json:"trigger,omitempty"`
	Prompt  string `yaml:"prompt" json:"prompt"`
	Default string `yaml:"default" json:"default,omitempty"`
}

type Handoff struct {
	Contract       string        `yaml:"contract" json:"contract"`
	Grabbable      []string      `yaml:"grabbable" json:"grabbable"`
	ReportBack     []interface{} `yaml:"report_back" json:"report_back"`
	LifecycleOwner string        `yaml:"lifecycle_owner" json:"lifecycle_owner"`
}

// AgentRuntimeConfig selects the repo-owned agent the embedded chat agent
// uses. Empty Default means the classic behavior (AGENTS.md / SYSTEM_PROMPT).
type AgentRuntimeConfig struct {
	Default string `yaml:"default" json:"default"`
}

// PipelineConfig declares the cross-kind derivation view. It renders the
// edges from `derivation`; the nav entry is what surfaces it in the UI.
type PipelineConfig struct {
	Nav *EngineNav `yaml:"nav" json:"nav,omitempty"`
}

type EngineConfig struct {
	ArtifactKinds map[string]ArtifactKind `yaml:"artifact_kinds" json:"artifact_kinds"`
	Derivation    []DerivationEdge        `yaml:"derivation" json:"derivation"`
	Replan        Replan                  `yaml:"replan" json:"replan"`
	Prompts       map[string]PromptDef    `yaml:"prompts" json:"prompts"`
	Handoff       Handoff                 `yaml:"handoff" json:"handoff"`
	AgentRuntime  AgentRuntimeConfig      `yaml:"agent_runtime" json:"agent_runtime"`
	Pipeline      PipelineConfig          `yaml:"pipeline" json:"pipeline"`
}

// engineConfig is the parsed config for the running server (legacy default
// repo). Per-repo configs live on Repo.Config; the global stays for classic
// single-repo behavior and tests.
var engineConfig EngineConfig

// ensureEngineConfig materializes the bundled default into
// $DEVTOP_DIR/config.yml when absent, so a fresh repo owns an editable copy.
// Never overwrites an existing config.
func ensureEngineConfig() (string, error) {
	return ensureEngineConfigIn(DEVTOP_DIR)
}

func ensureEngineConfigIn(devTop string) (string, error) {
	if err := os.MkdirAll(devTop, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(devTop, "config.yml")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.WriteFile(path, defaultEngineConfig, 0644); err != nil {
		return "", err
	}
	return path, nil
}

// ensureKindDirs creates the directory for each config-declared kind so the
// engine's generic artifact endpoints can serve an empty list on a fresh repo
// (main.go only mkdirs the classic docs/tickets/threads/data dirs).
func ensureKindDirs() error {
	for name, kind := range engineConfig.ArtifactKinds {
		if kind.Path == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Join(DEVTOP_DIR, kind.Path), 0755); err != nil {
			return fmt.Errorf("create kind dir for %s: %w", name, err)
		}
	}
	return nil
}

// toJSONable recursively converts yaml.v2-style nested maps
// (map[interface{}]interface{}) into string-keyed maps so encoding/json can
// marshal them. yaml.v2 uses map[interface{}]interface{} for nested mappings,
// which json rejects.
func toJSONable(v interface{}) interface{} {
	switch t := v.(type) {
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[fmt.Sprintf("%v", k)] = toJSONable(val)
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = toJSONable(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = toJSONable(val)
		}
		return out
	default:
		return v
	}
}

// loadEngineConfig parses $DEVTOP_DIR/config.yml into the legacy global,
// falling back to the bundled default (hermetic tests, config-less runs).
func loadEngineConfig() error {
	cfg, err := loadEngineConfigFrom(DEVTOP_DIR)
	if err != nil {
		return err
	}
	engineConfig = cfg
	return nil
}

// handleAPIEngineConfig serves the parsed engine config so the frontend can
// render nav sections and views from the active repo's declaration.
func handleAPIEngineConfig(w http.ResponseWriter, r *http.Request) {
	repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	cfg, err := repo.Config()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toJSONable(cfg))
}
