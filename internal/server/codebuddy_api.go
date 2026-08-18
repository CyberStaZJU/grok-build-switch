package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"grok_switch/internal/codebuddy"
	"grok_switch/internal/httpjson"
	"grok_switch/internal/profiles"
)

type codeBuddyStatus struct {
	Configured     bool     `json:"configured"`
	HasAPIKey      bool     `json:"has_api_key"`
	APIKeyMasked   string   `json:"api_key_masked,omitempty"`
	ProxyBaseURL   string   `json:"proxy_base_url"`
	ProxyPath      string   `json:"proxy_path"`
	ProfileID      string   `json:"profile_id,omitempty"`
	ProfileName    string   `json:"profile_name,omitempty"`
	DefaultModel   string   `json:"default_model"`
	Models         []string `json:"models"`
	Active         bool     `json:"active"`
	ActiveProvider string   `json:"active_provider_id,omitempty"`
	Note           string   `json:"note,omitempty"`
}

type codeBuddySaveRequest struct {
	APIKey       *string `json:"api_key"`
	DefaultModel string  `json:"default_model"`
	Activate     bool    `json:"activate"`
}

type codeBuddyTestRequest struct {
	APIKey string `json:"api_key"`
	Model  string `json:"model"`
}

func (s *Server) handleCodeBuddyAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		status, err := s.codeBuddyStatus()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, status)
	case http.MethodPut:
		var req codeBuddySaveRequest
		if err := httpjson.Decode(w, r, &req, httpjson.Options{MaxBytes: 1 << 20}); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		profile, err := s.saveCodeBuddy(req)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		status, err := s.codeBuddyStatus()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "profile_id": profile.ID, "status": status})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleCodeBuddyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req codeBuddyTestRequest
	if err := httpjson.Decode(w, r, &req, httpjson.Options{MaxBytes: 1 << 20, AllowEmpty: true}); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(req.APIKey)
	if key == "" {
		key = LoadCodeBuddyAPIKey(s.Profiles)
	}
	if key == "" {
		writeError(w, fmt.Errorf("缺少 CodeBuddy API Key"), http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = codebuddy.DefaultModel
	}
	if !codebuddy.IsKnownModel(model) {
		writeError(w, fmt.Errorf("不支持的模型 %q", model), http.StatusBadRequest)
		return
	}
	result, err := testCodeBuddyUpstream(r.Context(), key, model)
	if err != nil {
		writeJSONStatus(w, map[string]any{"ok": false, "error": err.Error(), "model": model}, http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "model": model, "reply": result})
}

func (s *Server) handleCodeBuddyActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		DefaultModel string `json:"default_model"`
	}
	if err := httpjson.Decode(w, r, &req, httpjson.Options{MaxBytes: 1 << 20, AllowEmpty: true}); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	key := LoadCodeBuddyAPIKey(s.Profiles)
	if key == "" {
		writeError(w, fmt.Errorf("请先保存 CodeBuddy API Key"), http.StatusBadRequest)
		return
	}
	profile, err := s.EnsureCodeBuddyProviderOpts(CodeBuddyEnsureOptions{
		APIKey:       key,
		DefaultModel: req.DefaultModel,
		Activate:     true,
	})
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	status, err := s.codeBuddyStatus()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "profile_id": profile.ID, "status": status})
}

func (s *Server) codeBuddyStatus() (codeBuddyStatus, error) {
	status := codeBuddyStatus{
		ProxyBaseURL: s.CodeBuddyProxyBaseURL(),
		ProxyPath:    "/codebuddy-proxy/v1",
		DefaultModel: codebuddy.DefaultModel,
		Models:       append([]string(nil), codebuddy.KnownModels...),
		Note:         "请求经 Switch 本机代理转发到 CodeBuddy，不经过 WorkBuddy agent harness。",
	}
	if s.ActualPort == 0 {
		status.ProxyBaseURL = "http://127.0.0.1:<port>/codebuddy-proxy/v1"
	}

	key := LoadCodeBuddyAPIKey(s.Profiles)
	if key != "" {
		status.HasAPIKey = true
		status.APIKeyMasked = maskSecret(key)
	}

	var profile *profiles.Profile
	if s.Profiles != nil {
		list, err := s.Profiles.List()
		if err != nil {
			return status, err
		}
		if found, ok := selectCodeBuddyProfile(list); ok {
			profile = &found
		}
	}
	if profile != nil {
		status.Configured = true
		status.ProfileID = profile.ID
		status.ProfileName = profile.Name
		if profile.DefaultModel != "" {
			status.DefaultModel = profile.DefaultModel
		}
		if strings.TrimSpace(profile.APIKey) != "" {
			status.HasAPIKey = true
			status.APIKeyMasked = maskSecret(profile.APIKey)
		}
	}

	if s.Routing != nil {
		snap, err := s.Routing.Snapshot()
		if err == nil {
			status.ActiveProvider = snap.ActiveProviderID
			if profile != nil && snap.ActiveProviderID == profile.ID {
				status.Active = true
				if policy, ok := snap.ProviderPolicies[profile.ID]; ok {
					if route, ok := snap.Route(policy.Default); ok {
						status.DefaultModel = route.ProfileModel
						if status.DefaultModel == "" {
							status.DefaultModel = route.Name
						}
					}
				}
			}
		} else if err != nil && err.Error() != "file does not exist" {
			// Snapshot may be missing before first init; ignore not-exist-like errors.
			if !isNotExist(err) {
				return status, err
			}
		}
	}
	return status, nil
}

func (s *Server) saveCodeBuddy(req codeBuddySaveRequest) (profiles.Profile, error) {
	existingKey := LoadCodeBuddyAPIKey(s.Profiles)
	key := existingKey
	if req.APIKey != nil {
		trimmed := strings.TrimSpace(*req.APIKey)
		if trimmed != "" {
			key = trimmed
		}
		// Explicit empty string with no existing key is invalid.
		if trimmed == "" && existingKey == "" {
			return profiles.Profile{}, fmt.Errorf("缺少 CodeBuddy API Key")
		}
	}
	if key == "" {
		return profiles.Profile{}, fmt.Errorf("缺少 CodeBuddy API Key")
	}
	return s.EnsureCodeBuddyProviderOpts(CodeBuddyEnsureOptions{
		APIKey:       key,
		DefaultModel: req.DefaultModel,
		Activate:     req.Activate,
	})
}

func testCodeBuddyUpstream(ctx context.Context, apiKey, model string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"stream":     true,
		"max_tokens": 16,
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly: pong"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codebuddy.DefaultUpstream, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("上游 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	// Read a few SSE chunks and collect content.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var content strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var obj map[string]any
		if json.Unmarshal([]byte(data), &obj) != nil {
			continue
		}
		choices, _ := obj["choices"].([]any)
		for _, chRaw := range choices {
			ch, _ := chRaw.(map[string]any)
			delta, _ := ch["delta"].(map[string]any)
			if c, ok := delta["content"].(string); ok {
				content.WriteString(c)
			}
		}
	}
	reply := strings.TrimSpace(content.String())
	if reply == "" {
		return "", fmt.Errorf("上游未返回内容")
	}
	return reply, nil
}

func maskSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	if len(secret) <= 8 {
		return "********"
	}
	return secret[:4] + "…" + secret[len(secret)-4:]
}

func isNotExist(err error) bool {
	if err == nil {
		return false
	}
	// os.ErrNotExist and path errors surface as "file does not exist" / "no such file".
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not exist") || strings.Contains(msg, "no such file")
}

