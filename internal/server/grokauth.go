package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"grok_switch/internal/grokauth"
	"grok_switch/internal/grokpool"
	"grok_switch/internal/profiles"
)

const grokAuthProfileName = "Grok Auth（本地代理）"

var defaultOfficialRoutingModels = []profiles.ModelDef{
	{
		Name:                    "grok-4.5",
		Model:                   "grok-4.5",
		APIBackend:              "official",
		SupportsBackendSearch:   true,
		SupportsReasoningEffort: true,
		ReasoningEfforts:        []string{"low", "medium", "high"},
		ReasoningEffortsSource:  "declared",
		ContextWindow:           500000,
		MaxCompletionTokens:     65536,
	},
}

var defaultGrokAuthModels = []profiles.ModelDef{
	{
		Name:                  "grok-4.5",
		Model:                 "grok-4.5",
		APIBackend:            "responses",
		SupportsBackendSearch: true,
		ContextWindow:         500000,
		MaxCompletionTokens:   65536,
	},
	{
		Name:                  "grok-composer-2.5-fast",
		Model:                 "grok-composer-2.5-fast",
		APIBackend:            "responses",
		SupportsBackendSearch: true,
		ContextWindow:         200000,
		MaxCompletionTokens:   32768,
	},
}

type grokAuthResponse struct {
	Configured       bool              `json:"configured"`
	SingleConfigured bool              `json:"single_configured"`
	PoolAccounts     int               `json:"pool_accounts"`
	Email            string            `json:"email,omitempty"`
	ExpiresAt        *time.Time        `json:"expires_at,omitempty"`
	NeedsRefresh     bool              `json:"needs_refresh"`
	LocalAPIKey      string            `json:"local_api_key,omitempty"`
	Source           string            `json:"source,omitempty"`
	ImportedAt       *time.Time        `json:"imported_at,omitempty"`
	LastRefresh      *time.Time        `json:"last_refresh,omitempty"`
	BaseURL          string            `json:"base_url,omitempty"`
	Profile          *profiles.Profile `json:"profile,omitempty"`
	Warning          string            `json:"warning,omitempty"`
}

func (s *Server) handleGrokAuth(w http.ResponseWriter, r *http.Request) {
	if s.GrokAuth == nil {
		writeError(w, fmt.Errorf("Grok auth store 未初始化"), http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		response, err := s.grokAuthStatusResponse()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, response)
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, fmt.Errorf("读取认证 JSON: %w", err), http.StatusBadRequest)
			return
		}
		if s.GrokPool != nil {
			if _, err := s.GrokPool.Import([]grokpool.ImportFile{{Name: "grok-auth-import.json", Content: string(raw)}}); err != nil {
				writeError(w, err, http.StatusBadRequest)
				return
			}
		}
		warnings := make([]string, 0)
		if s.GrokPool != nil {
			_ = s.GrokAuth.SetProxyURL(s.GrokPool.Status().Settings.ProxyURL)
		}
		if _, err := s.GrokAuth.Import(raw); err != nil {
			warnings = append(warnings, "统一号池已导入，但兼容单账号副本写入失败: "+err.Error())
		} else if _, err := s.GrokAuth.Token(r.Context()); err != nil {
			warnings = append(warnings, "凭据已导入号池，但兼容单账号 token 刷新失败: "+err.Error())
		}
		profile, err := s.upsertGrokAuthProfile()
		if err != nil {
			writeError(w, fmt.Errorf("凭据已导入，但创建本地 profile 失败: %w", err), http.StatusInternalServerError)
			return
		}
		response, err := s.grokAuthStatusResponse()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		response.Profile = &profile
		response.Warning = strings.Join(warnings, "；")
		s.changed()
		writeJSONStatus(w, response, http.StatusCreated)
	case http.MethodDelete:
		if err := s.GrokAuth.Delete(); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		var syncErr error
		if s.poolConfigured() {
			_, syncErr = s.upsertGrokAuthProfile()
		} else {
			syncErr = s.removeGrokAuthProfile()
		}
		if syncErr != nil {
			writeError(w, syncErr, http.StatusInternalServerError)
			return
		}
		s.changed()
		writeJSON(w, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleGrokAuthRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.GrokAuth == nil {
		writeError(w, fmt.Errorf("Grok auth store 未初始化"), http.StatusServiceUnavailable)
		return
	}
	if _, err := s.GrokAuth.Refresh(r.Context()); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeError(w, err, status)
		return
	}
	response, err := s.grokAuthStatusResponse()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, response)
}

func (s *Server) handleGrokProxy(w http.ResponseWriter, r *http.Request) {
	var token string
	var poolAccountID string
	var err error
	authorized := false
	if s.GrokPool != nil && s.GrokPool.Authorized(r) {
		sessionID := firstNonEmptyServer(r.Header.Get("x-grok-conv-id"), r.Header.Get("x-session-id"))
		token, poolAccountID, err = s.GrokPool.NextToken(r.Context(), sessionID)
		if err != nil && s.singleGrokAuthConfigured() {
			poolAccountID = ""
			token, err = s.GrokAuth.Token(r.Context())
		}
		authorized = true
	} else if s.GrokAuth != nil && s.GrokAuth.Authorized(r) {
		token, err = s.GrokAuth.Token(r.Context())
		authorized = true
	}
	if !authorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="grok_switch"`)
		writeError(w, fmt.Errorf("无效的本地 API Key"), http.StatusUnauthorized)
		return
	}
	if err != nil {
		writeError(w, err, http.StatusBadGateway)
		return
	}
	target, err := url.Parse(grokauth.UpstreamURL())
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}

	proxyRequest := r.Clone(r.Context())
	suffix := strings.TrimPrefix(r.URL.Path, "/grok/v1")
	if suffix == "" {
		suffix = "/"
	}
	proxyRequest.URL.Path = suffix
	proxyRequest.URL.RawPath = ""

	proxy := httputil.NewSingleHostReverseProxy(target)
	if s.GrokPool != nil {
		if transport := s.GrokPool.Transport(); transport != nil {
			proxy.Transport = transport
		}
	}
	originalDirector := proxy.Director
	proxy.Director = func(out *http.Request) {
		originalDirector(out)
		out.Host = target.Host
		out.Header.Del("x-api-key")
		out.Header.Set("Authorization", "Bearer "+token)
		out.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
		out.Header.Set("x-grok-client-version", "0.2.93")
		out.Header.Set("User-Agent", "xai-grok-workspace/0.2.93")
	}
	proxy.FlushInterval = -1
	proxy.ModifyResponse = func(response *http.Response) error {
		if s.GrokPool == nil || poolAccountID == "" || response.StatusCode < 400 {
			return nil
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			return nil
		}
		response.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), response.Body))
		s.GrokPool.ObserveResponse(poolAccountID, response.StatusCode, string(raw))
		return nil
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, proxyErr error) {
		if !errors.Is(proxyErr, context.Canceled) {
			writeError(writer, fmt.Errorf("Grok 上游请求失败: %w", proxyErr), http.StatusBadGateway)
		}
	}
	proxy.ServeHTTP(w, proxyRequest)
}

func (s *Server) ensureGrokAuthProfile() error {
	if s.ActualPort == 0 {
		return nil
	}
	_, configured, err := s.proxyAPIKey()
	if err != nil || !configured {
		return err
	}
	_, err = s.upsertGrokAuthProfile()
	return err
}

func (s *Server) upsertGrokAuthProfile() (profiles.Profile, error) {
	apiKey, configured, err := s.proxyAPIKey()
	if err != nil {
		return profiles.Profile{}, err
	}
	if !configured {
		return profiles.Profile{}, fmt.Errorf("尚未导入 Grok auth JSON 或号池账号")
	}
	baseURL := s.localGrokAuthURL()
	list, err := s.Profiles.List()
	if err != nil {
		return profiles.Profile{}, err
	}
	var existing *profiles.Profile
	for i := range list {
		if list[i].Name == grokAuthProfileName {
			existing = &list[i]
			break
		}
	}
	profile := profiles.Profile{
		Name:                   grokAuthProfileName,
		Template:               "responses",
		UpstreamFormat:         "openai_responses",
		BaseURL:                baseURL,
		APIKey:                 apiKey,
		AvailableModels:        modelNames(defaultGrokAuthModels),
		DefaultModel:           "grok-4.5",
		DefaultReasoningEffort: "low",
		Models:                 cloneModelDefs(defaultGrokAuthModels, baseURL, apiKey),
	}
	if existing == nil {
		return s.Profiles.Create(profile)
	}
	profile.DefaultModel = firstNonEmptyServer(existing.DefaultModel, profile.DefaultModel)
	profile.DefaultReasoningEffort = firstNonEmptyServer(existing.DefaultReasoningEffort, profile.DefaultReasoningEffort)
	if len(existing.Models) > 0 {
		profile.Models = cloneModelDefs(existing.Models, baseURL, apiKey)
		profile.AvailableModels = uniqueModelNames(append(existing.AvailableModels, modelNames(profile.Models)...))
	}
	updated, err := s.Profiles.Update(existing.ID, profile)
	if err != nil {
		return profiles.Profile{}, err
	}
	connectionChanged := existing.BaseURL != updated.BaseURL || existing.EffectiveAPIKey() != updated.EffectiveAPIKey()
	if connectionChanged && s.Switcher != nil {
		return s.Switcher.Activate(updated.ID)
	}
	return updated, nil
}

func (s *Server) removeGrokAuthProfile() error {
	list, err := s.Profiles.List()
	if err != nil {
		return err
	}
	for _, profile := range list {
		if profile.Name != grokAuthProfileName {
			continue
		}
		return s.Profiles.Delete(profile.ID)
	}
	return nil
}

func (s *Server) grokAuthStatusResponse() (grokAuthResponse, error) {
	status, err := s.GrokAuth.Status()
	if err != nil {
		return grokAuthResponse{}, err
	}
	response := grokAuthResponse{
		Configured:       status.Configured,
		SingleConfigured: status.Configured,
		Email:            status.Email,
		NeedsRefresh:     status.NeedsRefresh,
		LocalAPIKey:      status.LocalAPIKey,
		Source:           status.Source,
	}
	if s.GrokPool != nil {
		pool := s.GrokPool.Status()
		if pool.Configured {
			response.Configured = true
			response.PoolAccounts = len(pool.Accounts)
			response.LocalAPIKey = pool.LocalAPIKey
			response.Source = "unified-pool"
			response.NeedsRefresh = false
			if len(pool.Accounts) == 1 {
				response.Email = pool.Accounts[0].Email
			}
		}
	}
	if !status.ExpiresAt.IsZero() {
		expiresAt := status.ExpiresAt
		response.ExpiresAt = &expiresAt
	}
	if !status.ImportedAt.IsZero() {
		importedAt := status.ImportedAt
		response.ImportedAt = &importedAt
	}
	if !status.LastRefresh.IsZero() {
		lastRefresh := status.LastRefresh
		response.LastRefresh = &lastRefresh
	}
	if response.Configured {
		response.BaseURL = s.localGrokAuthURL()
	}
	return response, nil
}

func (s *Server) localGrokAuthURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/grok/v1", s.ActualPort)
}

func (s *Server) proxyAPIKey() (string, bool, error) {
	if s.GrokPool != nil {
		status := s.GrokPool.Status()
		if status.Configured {
			return status.LocalAPIKey, true, nil
		}
	}
	if s.GrokAuth == nil {
		return "", false, nil
	}
	status, err := s.GrokAuth.Status()
	if err != nil {
		return "", false, err
	}
	return status.LocalAPIKey, status.Configured, nil
}

func (s *Server) poolConfigured() bool {
	return s.GrokPool != nil && s.GrokPool.Status().Configured
}

func (s *Server) singleGrokAuthConfigured() bool {
	if s.GrokAuth == nil {
		return false
	}
	status, err := s.GrokAuth.Status()
	return err == nil && status.Configured
}

func cloneModelDefs(models []profiles.ModelDef, baseURL, apiKey string) []profiles.ModelDef {
	out := make([]profiles.ModelDef, len(models))
	for i, model := range models {
		out[i] = model
		out[i].BaseURL = baseURL
		out[i].APIKey = apiKey
		out[i].APIBackend = "responses"
		if model.ExtraHeaders != nil {
			out[i].ExtraHeaders = make(map[string]string, len(model.ExtraHeaders))
			for key, value := range model.ExtraHeaders {
				out[i].ExtraHeaders[key] = value
			}
		}
	}
	return out
}

func modelNames(models []profiles.ModelDef) []string {
	names := make([]string, 0, len(models))
	for _, model := range models {
		name := firstNonEmptyServer(model.Name, model.Model)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func uniqueModelNames(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func firstNonEmptyServer(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
