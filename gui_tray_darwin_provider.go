//go:build wailsgui && darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// routingSnapshot carries macOS menu-bar routing state.
type routingSnapshot struct {
	Official        bool          `json:"official"`
	DefaultModel    string        `json:"default_model"`
	WebSearchModel  string        `json:"web_search_model"`
	ExploreModel    string        `json:"explore_model"`
	PlanModel       string        `json:"plan_model"`
	AvailableModels []routingModel `json:"available_models"`
}

type routingModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s routingSnapshot) fingerprint() string {
	models := make([]string, 0, len(s.AvailableModels))
	for _, m := range s.AvailableModels {
		models = append(models, m.ID)
	}
	return fmt.Sprintf("%v|%s|%s|%s|%s|%s",
		s.Official, s.DefaultModel, s.WebSearchModel, s.ExploreModel, s.PlanModel,
		strings.Join(models, ","))
}

// cacheStatsSnapshot holds a summary of cache statistics for menu display.
type cacheStatsSnapshot struct {
	Turns              int     `json:"turns"`
	PromptTokens       int64   `json:"prompt_tokens"`
	CachedPromptTokens int64   `json:"cached_prompt_tokens"`
	CompletionTokens   int64   `json:"completion_tokens"`
	HitRate            *float64 `json:"hit_rate"`
}

func (s cacheStatsSnapshot) fingerprint() string {
	rate := "nil"
	if s.HitRate != nil {
		rate = fmt.Sprintf("%.4f", *s.HitRate)
	}
	return fmt.Sprintf("%d|%d|%d|%d|%s",
		s.Turns, s.PromptTokens, s.CachedPromptTokens, s.CompletionTokens, rate)
}

// darwinTrayProviderClient fetches routing state and cache stats from the local server.
type darwinTrayProviderClient struct {
	baseURL string
	client  *http.Client
}

func newDarwinTrayProviderClient(baseURL string) *darwinTrayProviderClient {
	return &darwinTrayProviderClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 3 * time.Second},
	}
}

// snapshot returns the full routing snapshot for macOS menu bar.
func (c *darwinTrayProviderClient) snapshot(ctx context.Context) (routingSnapshot, error) {
	var status struct {
		OfficialActive bool   `json:"official_active"`
		DefaultModel   string `json:"default_model"`
		WebSearchModel string `json:"web_search_model"`
		ExploreModel   string `json:"explore_model"`
		PlanModel      string `json:"plan_model"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/status", &status); err != nil {
		return routingSnapshot{}, err
	}

	var routingResp struct {
		ModelRoutes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"model_routes"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/routing", &routingResp); err != nil {
		return routingSnapshot{}, err
	}

	models := make([]routingModel, 0, len(routingResp.ModelRoutes))
	for _, m := range routingResp.ModelRoutes {
		models = append(models, routingModel{ID: m.ID, Name: m.Name})
	}

	return routingSnapshot{
		Official:        status.OfficialActive,
		DefaultModel:    status.DefaultModel,
		WebSearchModel:  status.WebSearchModel,
		ExploreModel:    status.ExploreModel,
		PlanModel:       status.PlanModel,
		AvailableModels: models,
	}, nil
}

// cacheStats fetches cache hit statistics.
func (c *darwinTrayProviderClient) cacheStats(ctx context.Context) (cacheStatsSnapshot, error) {
	var stats cacheStatsSnapshot
	if err := c.do(ctx, http.MethodGet, "/api/cache-stats?hours=24", &stats); err != nil {
		return cacheStatsSnapshot{}, err
	}
	return stats, nil
}

// updatePolicy sends a partial policy update to /api/routing/policy.
func (c *darwinTrayProviderClient) updatePolicy(ctx context.Context, patch map[string]any) error {
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return c.doRaw(ctx, http.MethodPut, "/api/routing/policy", body, nil)
}

func (c *darwinTrayProviderClient) do(ctx context.Context, method, path string, out any) error {
	return c.doRaw(ctx, method, path, nil, out)
}

func (c *darwinTrayProviderClient) doRaw(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var apiError struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiError)
		if apiError.Error != "" {
			return fmt.Errorf("%s", apiError.Error)
		}
		return fmt.Errorf("本地服务返回 HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("解析本地服务响应失败: %w", err)
	}
	return nil
}
