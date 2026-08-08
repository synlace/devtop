package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/frontmatter"
)

//go:embed templates/welcome.mdx
var welcomeDocContent string

// ensureWelcomeDoc materializes the embedded welcome doc as .devtop/docs/index.mdx
// on a fresh repository (no docs exist yet). Non-destructive: it never touches a
// repo that already has any docs, and it is skipped if the file already exists.
func ensureWelcomeDoc() error {
	if _, err := os.Stat(filepath.Join(DOCS_DIR, "index.mdx")); err == nil {
		return nil
	}
	docs, err := listDocs()
	if err != nil {
		return err
	}
	if len(docs) > 0 {
		return nil
	}
	_ = os.MkdirAll(DOCS_DIR, 0755)
	if err := os.WriteFile(filepath.Join(DOCS_DIR, "index.mdx"), []byte(welcomeDocContent), 0644); err != nil {
		return fmt.Errorf("writing welcome doc: %w", err)
	}
	return nil
}

// getWelcomeDoc serves the embedded welcome content directly (used when the
// materialized file is missing, e.g. on a read-only mount).
func getWelcomeDoc() (string, string, error) {
	var meta DocMeta
	bodyBytes, err := frontmatter.Parse(strings.NewReader(welcomeDocContent), &meta)
	if err != nil {
		return "", "", fmt.Errorf("parsing welcome doc: %w", err)
	}
	title := meta.Title
	if title == "" {
		title = "Welcome"
	}
	return title, strings.TrimSpace(string(bodyBytes)), nil
}
