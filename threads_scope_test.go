package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

// TestListThreads_RepoScope verifies threads are repo-global after migration:
// legacy page-scoped contexts (both repo-prefixed multi-repo and bare
// single-repo values) are listed under the owning repo and rewritten to the
// canonical key. listThreadsP walks one repo's threads dir, so a bare key
// found there — page-scoped, canonical-empty, or a rename artifact carrying an
// earlier repo name — belongs to that repo. Only CopilotKit's "global" orphan
// marker is excluded.
func TestListThreads_RepoScope(t *testing.T) {
	type tc struct {
		name      string
		context   string
		seeds     map[string]string // file id -> context
		want      []string          // listed ids, newest first
		rewritten map[string]string // file id -> context after listing
	}
	cases := []tc{
		{
			name:    "single-repo workspace",
			context: "",
			seeds: map[string]string{
				"single-legacy": "docs/index",
				"canonical":     "",
				"orphan":        "global",
			},
			want: []string{"single-legacy", "canonical"},
			rewritten: map[string]string{
				"single-legacy": "",
			},
		},
		{
			name:    "named repo",
			context: "tetris",
			seeds: map[string]string{
				"multi-legacy": "tetris:docs/index",
				"canonical":    "tetris",
				"other-repo":   "ferret", // rename artifact: an earlier repo name
				"orphan":       "global",
			},
			want: []string{"multi-legacy", "canonical", "other-repo"},
			rewritten: map[string]string{
				"multi-legacy": "tetris",
				"other-repo":   "tetris",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			p := RepoPaths{
				DevTop:  filepath.Join(root, ".devtop"),
				Threads: filepath.Join(root, ".devtop", "threads"),
			}
			if err := os.MkdirAll(p.Threads, 0755); err != nil {
				t.Fatal(err)
			}
			for id, ctx := range c.seeds {
				body, _ := json.Marshal(map[string]interface{}{
					"id":         id,
					"context":    ctx,
					"updated_at": "2026-08-11T19:00:00Z",
					"messages":   []interface{}{},
				})
				if err := os.WriteFile(filepath.Join(p.Threads, id+".json"), body, 0644); err != nil {
					t.Fatal(err)
				}
			}

			threads, err := listThreadsP(p, c.context)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(threads))
			for _, th := range threads {
				got = append(got, th["id"].(string))
			}
			sort.Strings(got)
			sort.Strings(c.want)
			if !slices.Equal(got, c.want) {
				t.Fatalf("listed %v, want %v", got, c.want)
			}

			for id, want := range c.rewritten {
				body, err := os.ReadFile(filepath.Join(p.Threads, id+".json"))
				if err != nil {
					t.Fatal(err)
				}
				var data map[string]interface{}
				if err := json.Unmarshal(body, &data); err != nil {
					t.Fatal(err)
				}
				if data["context"] != want {
					t.Fatalf("%s context = %v, want %q", id, data["context"], want)
				}
			}
		})
	}
}