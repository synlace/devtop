package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// initEngineConfigTestEnv creates a temp devtop dir and points DEVTOP_DIR at
// it, restoring the previous global afterwards so later tests (which derive
// paths from DEVTOP_DIR) see the original value.
func initEngineConfigTestEnv(t *testing.T) string {
	prev := DEVTOP_DIR
	tempDir := setupTestDirs(t)
	DEVTOP_DIR = tempDir
	t.Cleanup(func() {
		DEVTOP_DIR = prev
		os.RemoveAll(tempDir)
	})
	return tempDir
}

func TestEnsureEngineConfig_MaterializesDefault(t *testing.T) {
	initEngineConfigTestEnv(t)

	path, err := ensureEngineConfig()
	if err != nil {
		t.Fatalf("ensureEngineConfig: %v", err)
	}
	if path != filepath.Join(DEVTOP_DIR, "config.yml") {
		t.Errorf("unexpected path %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read materialized config: %v", err)
	}
	if !bytes.Equal(data, defaultEngineConfig) {
		t.Error("materialized config differs from the bundled default")
	}
}

func TestEnsureEngineConfig_NeverClobbers(t *testing.T) {
	initEngineConfigTestEnv(t)

	custom := []byte("artifact_kinds:\n  docs:\n    path: docs\n")
	path := filepath.Join(DEVTOP_DIR, "config.yml")
	if err := os.WriteFile(path, custom, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := ensureEngineConfig(); err != nil {
		t.Fatalf("ensureEngineConfig: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, custom) {
		t.Error("existing config was overwritten")
	}
}

func TestLoadEngineConfig_ParsesDefault(t *testing.T) {
	initEngineConfigTestEnv(t)

	if err := loadEngineConfig(); err != nil {
		t.Fatalf("loadEngineConfig: %v", err)
	}
	if len(engineConfig.ArtifactKinds) != 8 {
		t.Fatalf("expected 8 default kinds, got %d", len(engineConfig.ArtifactKinds))
	}
	intents, ok := engineConfig.ArtifactKinds["intents"]
	if !ok {
		t.Fatal("missing intents kind")
	}
	if !intents.RequiresApproval || intents.AgentWritable {
		t.Errorf("intents kind misconfigured: %+v", intents)
	}
	tickets, ok := engineConfig.ArtifactKinds["tickets"]
	if !ok {
		t.Fatal("missing tickets kind")
	}
	if tickets.Nav == nil || tickets.Nav.View != "board" {
		t.Errorf("tickets nav misconfigured: %+v", tickets.Nav)
	}
	documentation, ok := engineConfig.ArtifactKinds["documentation"]
	if !ok {
		t.Fatal("missing documentation kind")
	}
	if !documentation.RequiresApproval || !documentation.AgentWritable {
		t.Errorf("documentation kind misconfigured: %+v", documentation)
	}
	for _, k := range []string{"requirements", "decisions", "open_questions"} {
		if _, ok := engineConfig.ArtifactKinds[k]; !ok {
			t.Fatalf("missing %s kind", k)
		}
	}
	if _, ok := engineConfig.ArtifactKinds["prds"]; ok {
		t.Error("prds must be gone from the default config")
	}
	agents, ok := engineConfig.ArtifactKinds["agents"]
	if !ok {
		t.Fatal("missing agents kind")
	}
	if agents.Path != "agents" || agents.AgentWritable || agents.Nav != nil {
		t.Errorf("agents kind misconfigured: %+v", agents)
	}
	skills, ok := engineConfig.ArtifactKinds["skills"]
	if !ok {
		t.Fatal("missing skills kind")
	}
	if skills.Path != "skills" || skills.AgentWritable || skills.Nav != nil {
		t.Errorf("skills kind misconfigured: %+v", skills)
	}
	if engineConfig.AgentRuntime.Default != "docs" {
		t.Errorf("agent_runtime.default = %q, want the scaffolded docs agent", engineConfig.AgentRuntime.Default)
	}
	if len(engineConfig.Derivation) != 5 {
		t.Errorf("expected 5 derivation edges, got %d", len(engineConfig.Derivation))
	}
	if engineConfig.Derivation[0].Agent != "doc-builder" || engineConfig.Derivation[0].Transform != "describe_feature" {
		t.Errorf("intents->documentation edge misconfigured: %+v", engineConfig.Derivation[0])
	}
	if engineConfig.Derivation[0].Gate != "intents.review == approved" {
		t.Errorf("intents gate = %q", engineConfig.Derivation[0].Gate)
	}
	last := engineConfig.Derivation[len(engineConfig.Derivation)-1]
	if last.Agent != "ticket-deriver" || last.Gate != "requirements.review == approved" {
		t.Errorf("requirements->tickets edge misconfigured: %+v", last)
	}
	if engineConfig.Pipeline.Nav == nil || engineConfig.Pipeline.Nav.Label != "Work items" || engineConfig.Pipeline.Nav.View != "pipeline" {
		t.Errorf("pipeline nav misconfigured: %+v", engineConfig.Pipeline.Nav)
	}
	if engineConfig.Handoff.LifecycleOwner != "external" {
		t.Errorf("handoff lifecycle_owner = %q, want external", engineConfig.Handoff.LifecycleOwner)
	}
}

func TestLoadEngineConfig_PrefersRepoConfig(t *testing.T) {
	initEngineConfigTestEnv(t)

	// A repo config with an extra kind must win over the bundled default.
	cfg := `artifact_kinds:
  docs:
    path: docs
    extension: .mdx
    nav:
      label: Docs
      icon: file
      order: 1
      view: tree
  rfcs:
    path: rfcs
    extension: .md
    view: list
    nav:
      label: RFCs
      icon: doc
      order: 5
      view: list
`
	if err := os.WriteFile(filepath.Join(DEVTOP_DIR, "config.yml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := loadEngineConfig(); err != nil {
		t.Fatalf("loadEngineConfig: %v", err)
	}
	if _, ok := engineConfig.ArtifactKinds["rfcs"]; !ok {
		t.Error("repo-declared rfcs kind missing")
	}
	if _, ok := engineConfig.ArtifactKinds["tickets"]; ok {
		t.Error("repo config should replace, not merge with, the default")
	}
}

func TestLoadEngineConfig_InvalidYAML(t *testing.T) {
	initEngineConfigTestEnv(t)

	if err := os.WriteFile(filepath.Join(DEVTOP_DIR, "config.yml"), []byte("artifact_kinds: [unclosed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := loadEngineConfig(); err == nil {
		t.Fatal("expected an error for invalid YAML")
	}
}

func TestEnsureKindDirs(t *testing.T) {
	initEngineConfigTestEnv(t)

	if err := loadEngineConfig(); err != nil {
		t.Fatalf("loadEngineConfig: %v", err)
	}
	if err := ensureKindDirs(); err != nil {
		t.Fatalf("ensureKindDirs: %v", err)
	}
	for name, kind := range engineConfig.ArtifactKinds {
		if kind.Path == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(DEVTOP_DIR, kind.Path))
		if err != nil {
			t.Errorf("kind dir for %s not created: %v", name, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("kind path for %s is not a directory", name)
		}
	}
}

func TestAPIEngineConfig(t *testing.T) {
	tempDir, mux := initHTTPTestEnv(t)
	defer os.RemoveAll(tempDir)

	req := httptest.NewRequest("GET", "/api/engine-config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("expected JSON content type, got %q", ct)
	}
	var out struct {
		ArtifactKinds map[string]ArtifactKind `json:"artifact_kinds"`
		AgentRuntime  AgentRuntimeConfig      `json:"agent_runtime"`
		Pipeline      PipelineConfig          `json:"pipeline"`
		Handoff       Handoff                 `json:"handoff"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(out.ArtifactKinds) != 8 {
		t.Errorf("expected 8 kinds in response, got %d", len(out.ArtifactKinds))
	}
	if _, ok := out.ArtifactKinds["intents"]; !ok {
		t.Error("intents kind missing from response")
	}
	if _, ok := out.ArtifactKinds["requirements"]; !ok {
		t.Error("requirements kind missing from response")
	}
	if _, ok := out.ArtifactKinds["agents"]; !ok {
		t.Error("agents kind missing from response")
	}
	if _, ok := out.ArtifactKinds["skills"]; !ok {
		t.Error("skills kind missing from response")
	}
	if out.Pipeline.Nav == nil || out.Pipeline.Nav.Label != "Work items" {
		t.Error("pipeline nav missing from response")
	}
	if out.Handoff.LifecycleOwner != "external" {
		t.Errorf("lifecycle_owner = %q", out.Handoff.LifecycleOwner)
	}
}
