package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSameSourceArtifactFor_DedupesByDerivedFrom(t *testing.T) {
	tmp := t.TempDir()
	cfg := EngineConfig{ArtifactKinds: map[string]ArtifactKind{
		"documentation":  {Path: "documentation", Extension: ".mdx"},
		"open_questions": {Path: "open_questions", Extension: ".mdx"},
	}}
	p := RepoPaths{DevTop: tmp}

	existing := `---
title: Web based calculator
work_item: INT-1
derived_from: intents/INT-1
review: pending
---

# Web based calculator
`
	dst := filepath.Join(tmp, "documentation", "DOC-1")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "index.mdx"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	// A second write for the same source must resolve to the existing file.
	content := strings.Replace(existing, "\n# Web based calculator\n", "\n# Web based calculator (full)\n", 1)
	rel, id, ok := sameSourceArtifactFor(cfg, p, "documentation", content)
	if !ok {
		t.Fatal("expected same-source match, got none")
	}
	if id != "DOC-1" || rel != filepath.Join("documentation", "DOC-1", "index.mdx") {
		t.Fatalf("resolved %q / %q, want DOC-1 under documentation/", rel, id)
	}

	// When a newer sibling exists (DOC-1 stub written first, DOC-2 full doc
	// later), the newest matching artifact wins — the stub is not resurrected.
	dst2 := filepath.Join(tmp, "documentation", "DOC-2")
	if err := os.MkdirAll(dst2, 0755); err != nil {
		t.Fatal(err)
	}
	full2 := strings.Replace(existing, "DOC-1", "DOC-2", 1)
	full2 = strings.Replace(full2, "# Web based calculator\n", "# Web based calculator (full, second write)\n", 1)
	if err := os.WriteFile(filepath.Join(dst2, "index.mdx"), []byte(full2), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dst2, "index.mdx"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rel, id, ok = sameSourceArtifactFor(cfg, p, "documentation", content)
	if !ok || id != "DOC-2" {
		t.Fatalf("newest match = %q/%v, want DOC-2", rel, ok)
	}

	// A different source is not a duplicate.
	other := strings.Replace(content, "derived_from: intents/INT-1", "derived_from: intents/INT-2", 1)
	if _, _, ok := sameSourceArtifactFor(cfg, p, "documentation", other); ok {
		t.Fatal("different source must not dedupe")
	}

	// Content without derived_from never dedupes.
	if _, _, ok := sameSourceArtifactFor(cfg, p, "documentation", "# no frontmatter\n"); ok {
		t.Fatal("missing derived_from must not dedupe")
	}

	// Open questions are excluded: multiple questions per source are allowed.
	oqDir := filepath.Join(tmp, "open_questions")
	if err := os.MkdirAll(oqDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oqDir, "OQ-1.mdx"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := sameSourceArtifactFor(cfg, p, "open_questions", content); ok {
		t.Fatal("open_questions must not dedupe")
	}
}

func TestClarifySensitivityPrompt(t *testing.T) {
	low := clarifySensitivityPrompt("low")
	if !strings.Contains(low, "LOW") || !strings.Contains(low, "blocks") {
		t.Errorf("low prompt missing directive: %q", low)
	}
	high := clarifySensitivityPrompt("high")
	if !strings.Contains(high, "HIGH") || !strings.Contains(high, "liberally") {
		t.Errorf("high prompt missing directive: %q", high)
	}
	med := clarifySensitivityPrompt("")
	if !strings.Contains(med, "MEDIUM") || !strings.Contains(med, "outcome") {
		t.Errorf("default prompt missing medium directive: %q", med)
	}
	if clarifySensitivityPrompt("LOW") != low {
		t.Errorf("sensitivity should be case-insensitive")
	}
}
