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

func setupTestEnvironment(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "devtop-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	DEVTOP_DIR = tmpDir
	DOCS_DIR = filepath.Join(DEVTOP_DIR, "docs")
	TICKETS_DIR = filepath.Join(DEVTOP_DIR, "tickets")
	THREADS_DIR = filepath.Join(DEVTOP_DIR, "threads")
	DATA_DIR = filepath.Join(DEVTOP_DIR, "data")

	_ = os.MkdirAll(DOCS_DIR, 0755)
	_ = os.MkdirAll(TICKETS_DIR, 0755)
	_ = os.MkdirAll(THREADS_DIR, 0755)
	_ = os.MkdirAll(DATA_DIR, 0755)

	APP_DIR = "."
	STATIC_DIR = filepath.Join(APP_DIR, "frontend", "dist")

	// Create sample docs
	os.WriteFile(filepath.Join(DOCS_DIR, "index.mdx"), []byte(`---
title: "Project Overview"
---

# Welcome
Test documentation.
`), 0644)

	os.WriteFile(filepath.Join(DOCS_DIR, "architecture.mdx"), []byte(`---
title: "System Architecture"
---

# Architecture
Stack: Go + Alpine.
`), 0644)

	// Create sample tickets
	os.WriteFile(filepath.Join(TICKETS_DIR, "001.md"), []byte(`---
id: "001"
title: "Test ticket"
status: "open"
priority: "high"
assignee: "alice"
created: "2026-07-27"
---

Description text.

**2026-07-27 10:00** — alice: First comment.
`), 0644)

	return tmpDir
}

func TestDocAndTicketStore(t *testing.T) {
	tmpDir := setupTestEnvironment(t)
	defer os.RemoveAll(tmpDir)

	// Test GetDoc
	title, content, err := getDoc("architecture")
	if err != nil {
		t.Fatalf("failed to get doc: %v", err)
	}
	if title != "System Architecture" {
		t.Errorf("expected System Architecture, got %s", title)
	}
	if !strings.Contains(content, "Go + Alpine") {
		t.Errorf("expected Go + Alpine in content, got %s", content)
	}

	// Test ListDocs
	docs, err := listDocs()
	if err != nil {
		t.Fatalf("failed to list docs: %v", err)
	}
	if len(docs) != 2 {
		t.Errorf("expected 2 docs, got %d", len(docs))
	}

	// Test GetTicket
	ticket, err := getTicket("001")
	if err != nil {
		t.Fatalf("failed to get ticket: %v", err)
	}
	if ticket.Title != "Test ticket" {
		t.Errorf("expected Test ticket, got %s", ticket.Title)
	}
	if ticket.Priority != "high" {
		t.Errorf("expected high, got %s", ticket.Priority)
	}

	// Test Thread deletion from filesystem
	threadID := "thread-testdelete"
	threadFile := filepath.Join(THREADS_DIR, threadID+".json")
	_ = os.WriteFile(threadFile, []byte(`{"id":"thread-testdelete","title":"Test Delete"}`), 0644)

	_, err = getThread(threadID)
	if err != nil {
		t.Fatalf("expected thread to exist, got error: %v", err)
	}

	if err := deleteThreadFile(threadID); err != nil {
		t.Fatalf("failed to delete thread: %v", err)
	}

	if _, err := os.Stat(threadFile); !os.IsNotExist(err) {
		t.Errorf("expected thread file to be deleted from disk, but it still exists")
	}

	_, err = getThread(threadID)
	if err == nil {
		t.Errorf("expected thread to be deleted, but it still exists")
	}
}

func TestAPIRoutes(t *testing.T) {
	tmpDir := setupTestEnvironment(t)
	defer os.RemoveAll(tmpDir)

	// API Docs List
	req := httptest.NewRequest("GET", "/api/docs", nil)
	rr := httptest.NewRecorder()
	handleAPIDocs(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var docs []DocSlug
	if err := json.Unmarshal(rr.Body.Bytes(), &docs); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if len(docs) != 2 {
		t.Errorf("expected 2 docs, got %d", len(docs))
	}

	// API Doc Detail
	req = httptest.NewRequest("GET", "/api/docs/architecture", nil)
	req.SetPathValue("slug", "architecture")
	rr = httptest.NewRecorder()
	handleAPIDocPage(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var docDetail map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &docDetail); err != nil {
		t.Fatalf("failed to parse doc detail: %v", err)
	}
	if docDetail["title"] != "System Architecture" {
		t.Errorf("expected System Architecture, got %s", docDetail["title"])
	}

	// API Tickets List
	req = httptest.NewRequest("GET", "/api/tickets", nil)
	rr = httptest.NewRecorder()
	handleAPITickets(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var tickets []Ticket
	if err := json.Unmarshal(rr.Body.Bytes(), &tickets); err != nil {
		t.Fatalf("failed to parse tickets: %v", err)
	}
	if len(tickets) != 1 {
		t.Errorf("expected 1 ticket, got %d", len(tickets))
	}
}

func TestAPIThreads(t *testing.T) {
	tmpDir := setupTestEnvironment(t)
	defer os.RemoveAll(tmpDir)

	// Create Thread
	payload := map[string]string{
		"context": "global",
		"title":   "My thread",
	}
	bytesPayload, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/api/threads", bytes.NewReader(bytesPayload))
	rr := httptest.NewRecorder()
	handleAPICreateThread(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var thread map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &thread); err != nil {
		t.Fatalf("failed to parse thread: %v", err)
	}

	tid, _ := thread["id"].(string)
	if tid == "" {
		t.Fatalf("expected non-empty thread id")
	}

	// Get Thread
	req = httptest.NewRequest("GET", "/api/threads/"+tid, nil)
	req.SetPathValue("id", tid)
	rr = httptest.NewRecorder()
	handleAPIGetThread(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	// Delete Thread
	req = httptest.NewRequest("DELETE", "/api/threads/"+tid, nil)
	req.SetPathValue("id", tid)
	rr = httptest.NewRecorder()
	handleAPIDeleteThread(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected status 204 on delete, got %d", rr.Code)
	}

	// Verify Thread is Deleted
	req = httptest.NewRequest("GET", "/api/threads/"+tid, nil)
	req.SetPathValue("id", tid)
	rr = httptest.NewRecorder()
	handleAPIGetThread(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status 404 after delete, got %d", rr.Code)
	}
}