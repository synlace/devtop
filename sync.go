package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/frontmatter"
)

func writeDocToFileSystem(slug, content string) error {
	slug = strings.TrimSuffix(slug, ".mdx")
	var filePath string
	if strings.Contains(slug, "/") || slug == "index" {
		filePath = filepath.Join(DOCS_DIR, slug+".mdx")
	} else {
		filePath = filepath.Join(DOCS_DIR, slug, "index.mdx")
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(content), 0644)
}

func writeTicketToFileSystem(t Ticket) error {
	filePath := filepath.Join(TICKETS_DIR, t.ID+".md")
	fm := fmt.Sprintf(`---
id: "%s"
title: "%s"
status: "%s"
priority: "%s"
assignee: "%s"
created: "%s"
---

%s`, t.ID, t.Title, t.Status, t.Priority, t.Assignee, t.Created, strings.TrimSpace(t.RawDescription))
	return os.WriteFile(filePath, []byte(fm), 0644)
}

func writeThreadToFileSystem(id string, threadData map[string]interface{}) error {
	filePath := filepath.Join(THREADS_DIR, id+".json")
	bytes, err := json.MarshalIndent(threadData, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, bytes, 0644)
}

func deleteThreadFile(id string) error {
	filePath := filepath.Join(THREADS_DIR, id+".json")
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func getNextTicketID() string {
	files, _ := filepath.Glob(filepath.Join(TICKETS_DIR, "*.md"))
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