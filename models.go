package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type APIModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ModelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

type ModelCache struct {
	Models   []ModelInfo `json:"models"`
	CachedAt string      `json:"cached_at"`
}

func loadCachedModelsIn(dataDir string) []ModelInfo {
	if dataDir == "" {
		return nil
	}
	cachePath := filepath.Join(dataDir, "models.json")
	if _, err := os.Stat(cachePath); err != nil {
		return nil
	}

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil
	}

	var cache ModelCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	return cache.Models
}

func cacheModelsIn(dataDir string, models []ModelInfo) {
	if dataDir == "" {
		return
	}
	cachePath := filepath.Join(dataDir, "models.json")
	_ = os.MkdirAll(dataDir, 0755)

	randBytes := make([]byte, 4)
	_, _ = rand.Read(randBytes)

	cache := ModelCache{
		Models:   models,
		CachedAt: hex.EncodeToString(randBytes),
	}

	bytes, _ := json.Marshal(cache)
	_ = os.WriteFile(cachePath, bytes, 0644)
}

func fetchModels(baseURL, apiKey, dataDir string) ([]ModelInfo, error) {
	cached := loadCachedModelsIn(dataDir)
	if len(cached) > 0 {
		return cached, nil
	}

	client := &http.Client{Timeout: 10 * time.Second}
	url := strings.TrimSuffix(baseURL, "/") + "/models"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return cached, err
	}

	if apiKey != "" && apiKey != "not-needed" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return cached, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return cached, nil
	}

	var apiResp struct {
		Data []struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Name  string `json:"name"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return cached, err
	}

	provider := "other"
	if strings.Contains(baseURL, "openrouter") {
		provider = "openrouter"
	} else if strings.Contains(baseURL, "localhost") || strings.Contains(baseURL, "127.0.0.1") {
		provider = "lmstudio"
	}

	var models []ModelInfo
	for _, m := range apiResp.Data {
		mid := m.ID
		if mid == "" {
			mid = m.Model
		}
		if mid == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = mid
		}
		models = append(models, ModelInfo{
			ID:       mid,
			Name:     name,
			Provider: provider,
		})
	}

	cacheModelsIn(dataDir, models)
	return models, nil
}
