package main

import (
	"strings"
	"testing"
)

func TestToolArgsDigest(t *testing.T) {
	long := strings.Repeat("x", 300)
	d := toolArgsDigest(map[string]interface{}{
		"kind":        "documentation",
		"id":          "DOC-1",
		"content":     long,
		"description": "desc-text",
		"ignored":     "not recorded",
		"path":        "/etc/passwd",
	})
	if d["kind"] != "documentation" || d["id"] != "DOC-1" {
		t.Errorf("scalar args dropped: %+v", d)
	}
	if len(d["content"]) != 200 {
		t.Errorf("content digest length = %d, want 200", len(d["content"]))
	}
	if d["description"] != "desc-text" {
		t.Errorf("description digest = %+v", d)
	}
	if _, ok := d["ignored"]; ok {
		t.Errorf("non-declared arg leaked into digest: %+v", d)
	}
	// Empty scalar values are omitted.
	d2 := toolArgsDigest(map[string]interface{}{"kind": ""})
	if len(d2) != 0 {
		t.Errorf("empty args not pruned: %+v", d2)
	}
	if len(toolArgsDigest(nil)) != 0 {
		t.Errorf("nil args must yield an empty digest")
	}
}

func TestIsToolError(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Error: not a git repository at /x", "Error: not a git repository at /x"},
		{"error: cannot determine target path", "error: cannot determine target path"},
		{"Written to .devtop/documentation/DOC-1 (id=DOC-1)", ""},
		{"Nothing to commit — no changes detected.", ""},
		{"Created ticket 1: title", ""},
	} {
		got := isToolError(c.in)
		if got != (c.want != "") {
			t.Errorf("isToolError(%q) = %v, want %v", c.in, got, c.want != "")
		}
	}
}
