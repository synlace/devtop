package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/frontmatter"
)

// docTitleFromContent extracts the frontmatter title of a doc's full MDX
// source, or "" when there is no parseable title.
func docTitleFromContent(content string) string {
	var meta DocMeta
	if _, err := frontmatter.Parse(strings.NewReader(content), &meta); err != nil {
		return ""
	}
	return meta.Title
}

func writeDocToFileSystemP(p RepoPaths, slug, content string) error {
	slug = strings.TrimSuffix(slug, ".mdx")
	if _, err := guardPath(p.Docs, slug); err != nil {
		return err
	}

	// Overwrite in place: resolve the existing file with the same rules the
	// read paths use (docPathForSlug/getDoc), so a doc that lives at
	// <slug>/index.mdx is updated there, and one at <slug>.mdx stays there.
	if existing, err := docPathForSlugP(p, slug); err == nil {
		return os.WriteFile(existing, []byte(content), 0644)
	}

	// New file: same placement rules as before.
	var filePath string
	if strings.Contains(slug, "/") || slug == "index" {
		filePath = filepath.Join(p.Docs, slug+".mdx")
	} else {
		filePath = filepath.Join(p.Docs, slug, "index.mdx")
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(content), 0644)
}

func writeTicketToFileSystemP(p RepoPaths, t Ticket) error {
	if _, err := guardPath(p.Tickets, t.ID+".md"); err != nil {
		return err
	}
	filePath := filepath.Join(p.Tickets, t.ID+".md")
	fm := fmt.Sprintf(`---
id: "%s"
title: "%s"
status: "%s"
priority: "%s"
assignee: "%s"
created: "%s"
source: "%s"
---

%s`, t.ID, t.Title, t.Status, t.Priority, t.Assignee, t.Created, t.Source, strings.TrimSpace(t.RawDescription))
	return os.WriteFile(filePath, []byte(fm), 0644)
}

func writeThreadToFileSystemP(p RepoPaths, id string, threadData map[string]interface{}) error {
	if _, err := guardPath(p.Threads, id+".json"); err != nil {
		return err
	}
	filePath := filepath.Join(p.Threads, id+".json")
	bytes, err := json.MarshalIndent(threadData, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, bytes, 0644)
}

func deleteThreadFileP(p RepoPaths, id string) error {
	if _, err := guardPath(p.Threads, id+".json"); err != nil {
		return err
	}
	filePath := filepath.Join(p.Threads, id+".json")
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func getNextTicketIDP(p RepoPaths) string {
	files, _ := filepath.Glob(filepath.Join(p.Tickets, "*.md"))
	if len(files) == 0 {
		return "001"
	}
	maxID := 0
	for _, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f), ".md")
		if len(stem) == 3 {
			var n int
			if _, err := fmt.Sscanf(stem, "%d", &n); err == nil && n > maxID {
				maxID = n
			}
		}
	}
	return fmt.Sprintf("%03d", maxID+1)
}

func parseFrontmatterFile(filePath string, meta interface{}) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return frontmatter.Parse(file, meta)
}

// Legacy global-backed variants (classic single-repo mode; tools).

func writeDocToFileSystem(slug, content string) error {
	return writeDocToFileSystemP(defaultPaths(), slug, content)
}

func writeTicketToFileSystem(t Ticket) error { return writeTicketToFileSystemP(defaultPaths(), t) }

func writeThreadToFileSystem(id string, threadData map[string]interface{}) error {
	return writeThreadToFileSystemP(defaultPaths(), id, threadData)
}

func deleteThreadFile(id string) error { return deleteThreadFileP(defaultPaths(), id) }

func getNextTicketID() string { return getNextTicketIDP(defaultPaths()) }
