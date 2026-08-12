package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// templates holds the scaffold sources: .devtop is the source of truth and is
// materialized from them at init. After materialization the runtime reads only
// the repo's .devtop/ files — never these embedded templates.
//
//go:embed templates
var scaffoldFS embed.FS

// scaffoldRepo materializes the complete .devtop scaffold for one repo:
// storage dirs, config.yml, config-declared kind dirs, the default agents,
// the default skills, and the welcome doc. It is the single write point —
// both the UI "Initialize" action (Repo.Init) and the classic-mode boot seed
// call it. Non-destructive: existing files are never overwritten.
func scaffoldRepo(p RepoPaths) error {
	for _, d := range []string{p.DevTop, p.Docs, p.Tickets, p.Threads, p.Data} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}
	if _, err := ensureEngineConfigIn(p.DevTop); err != nil {
		return err
	}
	cfg, err := loadEngineConfigFrom(p.DevTop)
	if err != nil {
		return err
	}
	for name, kind := range cfg.ArtifactKinds {
		if kind.Path == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Join(p.DevTop, kind.Path), 0755); err != nil {
			return fmt.Errorf("create kind dir for %s: %w", name, err)
		}
	}
	for _, kind := range []string{"agents", "skills"} {
		if err := scaffoldKind(cfg, p, kind); err != nil {
			return err
		}
	}
	if err := ensureRootAgentsFile(p); err != nil {
		return err
	}
	return ensureWelcomeDocIn(p)
}

// ensureRootAgentsFile writes the implementation contract to the repo root
// (next to .devtop) when no AGENTS.md exists. External coding agents read it
// to claim, implement, and close tickets. Repo-authored files win.
func ensureRootAgentsFile(p RepoPaths) error {
	dst := filepath.Join(filepath.Dir(p.DevTop), "AGENTS.md")
	if fi, err := os.Stat(dst); err == nil && !fi.IsDir() {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	data, err := fs.ReadFile(scaffoldFS, "templates/AGENTS.md")
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// scaffoldKind materializes the default templates for one artifact kind
// (agents, skills) into the repo, writing each file only when a file of that
// name does not already exist. Files already present are repo-owned and are
// never touched.
func scaffoldKind(cfg EngineConfig, p RepoPaths, kind string) error {
	k, ok := cfg.ArtifactKinds[kind]
	if !ok || k.Path == "" {
		return nil
	}
	assetDir := "templates/" + kind
	entries, err := fs.ReadDir(scaffoldFS, assetDir)
	if err != nil {
		return nil // no templates for this kind
	}
	dir := filepath.Join(p.DevTop, k.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dst := filepath.Join(dir, e.Name())
		if fi, err := os.Stat(dst); err == nil && !fi.IsDir() {
			continue // repo-authored file wins
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		data, err := fs.ReadFile(scaffoldFS, filepath.ToSlash(filepath.Join(assetDir, e.Name())))
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return err
		}
	}
	return nil
}