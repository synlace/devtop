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
}

func docSlugFromPath(path string) string {
	rel, err := filepath.Rel(DOCS_DIR, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasSuffix(rel, "/index.mdx") {
		return strings.TrimSuffix(rel, "/index.mdx")
	}
	return strings.TrimSuffix(rel, ".mdx")
}

func listDocs() ([]DocSlug, error) {
	docs := []DocSlug{}
	err := filepath.WalkDir(DOCS_DIR, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".mdx") {
			return nil
		}
		slug := docSlugFromPath(path)
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

func getDoc(slug string) (string, string, error) {
	slug = strings.TrimSuffix(slug, ".mdx")
	var filePath string
	if strings.Contains(slug, "/") {
		filePath = filepath.Join(DOCS_DIR, slug+".mdx")
	} else {
		filePath = filepath.Join(DOCS_DIR, slug+".mdx")
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			filePath = filepath.Join(DOCS_DIR, slug, "index.mdx")
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
	return title, renderMD(string(bodyBytes)), nil
}

func listTickets() ([]Ticket, error) {
	tickets := []Ticket{}
	err := filepath.WalkDir(TICKETS_DIR, func(path string, d os.DirEntry, err error) error {
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
			RawDescription: string(bodyBytes),
			Description:    renderMD(string(bodyBytes)),
		})
		return nil
	})
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].ID < tickets[j].ID })
	return tickets, err
}

func getTicket(id string) (Ticket, error) {
	filePath := filepath.Join(TICKETS_DIR, id+".md")
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

func listThreads(context string) ([]map[string]interface{}, error) {
	var threads []map[string]interface{}
	err := filepath.WalkDir(THREADS_DIR, func(path string, d os.DirEntry, err error) error {
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

func getThread(id string) (map[string]interface{}, error) {
	filePath := filepath.Join(THREADS_DIR, id+".json")
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

func deleteThread(id string) error {
	filePath := filepath.Join(THREADS_DIR, id+".json")
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}