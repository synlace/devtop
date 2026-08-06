package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func setupTestDirs(t *testing.T) string {
	tempDir, err := os.MkdirTemp("", "devtop-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	DATA_DIR = filepath.Join(tempDir, "data")
	DOCS_DIR = filepath.Join(tempDir, "docs")
	TICKETS_DIR = filepath.Join(tempDir, "tickets")
	THREADS_DIR = filepath.Join(tempDir, "threads")

	return tempDir
}

func TestModelCache_CacheAndLoad(t *testing.T) {
	tempDir := setupTestDirs(t)
	defer os.RemoveAll(tempDir)

	models := []ModelInfo{
		{ID: "model-a", Name: "Model A", Provider: "test"},
	}

	cacheModels(models)
	loaded := loadCachedModels()

	if len(loaded) != 1 {
		t.Fatalf("expected 1 model, got %d", len(loaded))
	}
	if loaded[0].ID != "model-a" || loaded[0].Name != "Model A" || loaded[0].Provider != "test" {
		t.Errorf("loaded model mismatch: %+v", loaded[0])
	}
}

func TestModelCache_CacheEmptyList(t *testing.T) {
	tempDir := setupTestDirs(t)
	defer os.RemoveAll(tempDir)

	cacheModels([]ModelInfo{})
	loaded := loadCachedModels()

	if loaded == nil {
		t.Fatal("expected non-nil slice, got nil")
	}
	if len(loaded) != 0 {
		t.Errorf("expected 0 models, got %d", len(loaded))
	}
}

func TestModelCache_CacheOverwrites(t *testing.T) {
	tempDir := setupTestDirs(t)
	defer os.RemoveAll(tempDir)

	cacheModels([]ModelInfo{{ID: "v1"}})
	cacheModels([]ModelInfo{{ID: "v2"}})
	loaded := loadCachedModels()

	if len(loaded) != 1 || loaded[0].ID != "v2" {
		t.Errorf("expected v2, got %+v", loaded)
	}
}

func TestModelCache_LoadNoCacheFile(t *testing.T) {
	tempDir := setupTestDirs(t)
	defer os.RemoveAll(tempDir)

	loaded := loadCachedModels()
	if loaded != nil {
		t.Errorf("expected nil when cache file is missing, got %+v", loaded)
	}
}

func TestModelCache_LoadCorruptedCache(t *testing.T) {
	tempDir := setupTestDirs(t)
	defer os.RemoveAll(tempDir)

	_ = os.MkdirAll(DATA_DIR, 0755)
	cachePath := filepath.Join(DATA_DIR, "models.json")
	_ = os.WriteFile(cachePath, []byte("corrupted json"), 0644)

	loaded := loadCachedModels()
	if loaded != nil {
		t.Errorf("expected nil when cache file is corrupted, got %+v", loaded)
	}
}

func TestModelCache_CacheCreatesDataDir(t *testing.T) {
	tempDir := setupTestDirs(t)
	defer os.RemoveAll(tempDir)

	// Explicitly remove data directory
	_ = os.RemoveAll(DATA_DIR)

	cacheModels([]ModelInfo{{ID: "test"}})

	if _, err := os.Stat(filepath.Join(DATA_DIR, "models.json")); os.IsNotExist(err) {
		t.Fatal("expected cache file to be created, but it was not")
	}
}

func TestFetchModels_UsesCacheOnSuccess(t *testing.T) {
	tempDir := setupTestDirs(t)
	defer os.RemoveAll(tempDir)

	cachedModels := []ModelInfo{{ID: "cached-model", Name: "Cached", Provider: "openrouter"}}
	cacheModels(cachedModels)

	// Invalid URL should not trigger error or network call because cache is warm and used directly
	result, err := fetchModels("http://invalid-localhost-url-not-exist:12345/v1", "sk-test")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result) != 1 || result[0].ID != "cached-model" {
		t.Errorf("expected cached models, got %+v", result)
	}
}

func TestFetchModels_ReturnsErrorOnEmptyCacheFailure(t *testing.T) {
	tempDir := setupTestDirs(t)
	defer os.RemoveAll(tempDir)

	// Cache is empty. This will try to make network request.
	result, err := fetchModels("http://invalid-localhost-url-not-exist:12345/v1", "sk-test")
	if err == nil {
		t.Fatal("expected connection error, got nil")
	}

	if len(result) != 0 {
		t.Errorf("expected 0 models, got %d", len(result))
	}
}

func TestFetchModels_OpenRouter(t *testing.T) {
	tempDir := setupTestDirs(t)
	defer os.RemoveAll(tempDir)

	// Spin up test server simulating OpenRouter API
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "openai/gpt-4o", "name": "GPT-4o"},
				{"id": "anthropic/claude-3", "name": "Claude 3"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	result, err := fetchModels(srv.URL, "sk-test")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 models, got %d", len(result))
	}

	if result[0].ID != "openai/gpt-4o" || result[0].Name != "GPT-4o" {
		t.Errorf("first model mismatch: %+v", result[0])
	}
	if result[1].ID != "anthropic/claude-3" || result[1].Name != "Claude 3" {
		t.Errorf("second model mismatch: %+v", result[1])
	}
}
