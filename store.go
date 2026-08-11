package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/adrg/frontmatter"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

type DocSlug struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

type Ticket struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Status         string    `json:"status"`
	Priority       string    `json:"priority"`
	Assignee       string    `json:"assignee"`
	Created        string    `json:"created"`
	Source         string    `json:"source"`
	Description    string    `json:"description"`
	RawDescription string    `json:"raw_description"`
	Comments       []Comment `json:"comments"`
}

type Comment struct {
	Date   string `json:"date"`
	Author string `json:"author,omitempty"`
	Text   string `json:"text"`
}

var mdRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

func renderMD(text string) string {
	var buf strings.Builder
	if err := mdRenderer.Convert([]byte(text), &buf); err != nil {
		return text
	}
	return buf.String()
}

func frontmatterParse(file *os.File, meta interface{}) ([]byte, error) {
	return frontmatter.Parse(file, meta)
}

type DocMeta struct {
	Title string `yaml:"title"`
}

type TicketMeta struct {
	ID       string `yaml:"id"`
	Title    string `yaml:"title"`
	Status   string `yaml:"status"`
	Priority string `yaml:"priority"`
	Assignee string `yaml:"assignee"`
	Created  string `yaml:"created"`
	Source   string `yaml:"source"`
}

func docSlugFromPath(p RepoPaths, path string) string {
	rel, err := filepath.Rel(p.Docs, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasSuffix(rel, "/index.mdx") {
		return strings.TrimSuffix(rel, "/index.mdx")
	}
	return strings.TrimSuffix(rel, ".mdx")
}

func listDocsP(p RepoPaths) ([]DocSlug, error) {
	docs := []DocSlug{}
	err := filepath.WalkDir(p.Docs, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".mdx") {
			return nil
		}
		slug := docSlugFromPath(p, path)
		title := slug
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer file.Close()
		var meta DocMeta
		if _, parseErr := frontmatterParse(file, &meta); parseErr == nil && meta.Title != "" {
			title = meta.Title
		}
		docs = append(docs, DocSlug{Slug: slug, Title: title})
		return nil
	})
	sort.Slice(docs, func(i, j int) bool { return docs[i].Slug < docs[j].Slug })
	return docs, err
}

func getDocP(p RepoPaths, slug string) (string, string, error) {
	slug = strings.TrimSuffix(slug, ".mdx")
	if _, err := guardPath(p.Docs, slug); err != nil {
		return "", "", err
	}
	var filePath string
	if strings.Contains(slug, "/") {
		filePath = filepath.Join(p.Docs, slug+".mdx")
	} else {
		filePath = filepath.Join(p.Docs, slug+".mdx")
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			filePath = filepath.Join(p.Docs, slug, "index.mdx")
		}
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", "", fmt.Errorf("document not found: %s", slug)
	}
	defer file.Close()
	var meta DocMeta
	bodyBytes, err := frontmatterParse(file, &meta)
	if err != nil {
		bodyBytes = []byte{}
	}
	title := meta.Title
	if title == "" {
		title = slug
	}
	// Serve the raw Markdown body (frontmatter stripped); rendering is the
	// client's job so the file on disk is always the source of truth.
	return title, strings.TrimSpace(string(bodyBytes)), nil
}

// Resolve a slug to its file on disk: <slug>.mdx, falling back to
// <slug>/index.mdx like getDoc. Guards against path traversal.
func resolveDocPathP(p RepoPaths, slug string) (string, error) {
	slug = strings.TrimSuffix(slug, ".mdx")
	if slug == "" || strings.Contains(slug, "..") {
		return "", fmt.Errorf("invalid slug: %s", slug)
	}
	direct := filepath.Join(p.Docs, slug+".mdx")
	if _, err := os.Stat(direct); err == nil {
		return direct, nil
	}
	idx := filepath.Join(p.Docs, slug, "index.mdx")
	if _, err := os.Stat(idx); err == nil {
		return idx, nil
	}
	return "", fmt.Errorf("document not found: %s", slug)
}

func deleteDocP(p RepoPaths, slug string) error {
	path, err := resolveDocPathP(p, slug)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// Favourites — Option B: a user-scoped, never-committed list beside config
// (.devtop/favourites.json). Stale slugs (doc deleted or renamed) are dropped
// on load so the store can never point at nothing.
func listFavouritesP(p RepoPaths) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(p.DevTop, "favourites.json"))
	if err != nil {
		return []string{}, nil
	}
	var slugs []string
	if err := json.Unmarshal(data, &slugs); err != nil {
		return []string{}, nil
	}
	seen := make(map[string]bool)
	out := []string{}
	for _, s := range slugs {
		if seen[s] {
			continue
		}
		if _, err := resolveDocPathP(p, s); err != nil {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

func saveFavouritesP(p RepoPaths, slugs []string) error {
	data, err := json.Marshal(slugs)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.DevTop, "favourites.json"), data, 0644)
}

func listTicketsP(p RepoPaths) ([]Ticket, error) {
	tickets := []Ticket{}
	err := filepath.WalkDir(p.Tickets, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer file.Close()
		var meta TicketMeta
		bodyBytes, parseErr := frontmatterParse(file, &meta)
		if parseErr != nil {
			return nil
		}
		id := meta.ID
		if id == "" {
			id = strings.TrimSuffix(filepath.Base(path), ".md")
		}
		status := meta.Status
		if status == "" {
			status = "open"
		}
		priority := meta.Priority
		if priority == "" {
			priority = "medium"
		}
		tickets = append(tickets, Ticket{
			ID:             id,
			Title:          meta.Title,
			Status:         status,
			Priority:       priority,
			Assignee:       meta.Assignee,
			Created:        meta.Created,
			Source:         meta.Source,
			RawDescription: string(bodyBytes),
			Description:    renderMD(string(bodyBytes)),
		})
		return nil
	})
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].ID < tickets[j].ID })
	return tickets, err
}

func getTicketP(p RepoPaths, id string) (Ticket, error) {
	filePath := filepath.Join(p.Tickets, id+".md")
	file, err := os.Open(filePath)
	if err != nil {
		return Ticket{}, fmt.Errorf("ticket not found: %s", id)
	}
	defer file.Close()
	var meta TicketMeta
	bodyBytes, err := frontmatterParse(file, &meta)
	if err != nil {
		return Ticket{}, fmt.Errorf("ticket not found: %s", id)
	}
	t := Ticket{
		ID:             meta.ID,
		Title:          meta.Title,
		Status:         meta.Status,
		Priority:       meta.Priority,
		Assignee:       meta.Assignee,
		Created:        meta.Created,
		Source:         meta.Source,
		RawDescription: string(bodyBytes),
		Description:    renderMD(string(bodyBytes)),
	}
	if t.Status == "" {
		t.Status = "open"
	}
	if t.Priority == "" {
		t.Priority = "medium"
	}
	return t, nil
}

func listThreadsP(p RepoPaths, context string) ([]map[string]interface{}, error) {
	var threads []map[string]interface{}
	err := filepath.WalkDir(p.Threads, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var data map[string]interface{}
		if err := json.Unmarshal(content, &data); err != nil {
			return nil
		}
		ctx, _ := data["context"].(string)
		if context != "" && ctx != context {
			return nil
		}
		threads = append(threads, data)
		return nil
	})
	sort.Slice(threads, func(i, j int) bool {
		ti, _ := threads[i]["updated_at"].(string)
		tj, _ := threads[j]["updated_at"].(string)
		return ti > tj
	})
	return threads, err
}

func getThreadP(p RepoPaths, id string) (map[string]interface{}, error) {
	filePath := filepath.Join(p.Threads, id+".json")
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("thread not found: %s", id)
	}
	var data map[string]interface{}
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, fmt.Errorf("thread not found: %s", id)
	}
	return data, nil
}

func deleteThreadP(p RepoPaths, id string) error {
	filePath := filepath.Join(p.Threads, id+".json")
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Legacy global-backed variants (classic single-repo mode; tests).

func listDocs() ([]DocSlug, error) { return listDocsP(defaultPaths()) }

func getDoc(slug string) (string, string, error) { return getDocP(defaultPaths(), slug) }

func resolveDocPath(slug string) (string, error) { return resolveDocPathP(defaultPaths(), slug) }

func deleteDoc(slug string) error { return deleteDocP(defaultPaths(), slug) }

func listFavourites() ([]string, error) { return listFavouritesP(defaultPaths()) }

func saveFavourites(slugs []string) error { return saveFavouritesP(defaultPaths(), slugs) }

func listTickets() ([]Ticket, error) { return listTicketsP(defaultPaths()) }

func getTicket(id string) (Ticket, error) { return getTicketP(defaultPaths(), id) }

func listThreads(context string) ([]map[string]interface{}, error) {
	return listThreadsP(defaultPaths(), context)
}

func getThread(id string) (map[string]interface{}, error) { return getThreadP(defaultPaths(), id) }

func deleteThread(id string) error { return deleteThreadP(defaultPaths(), id) }
