package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"grok_switch/internal/httpjson"
	"grok_switch/internal/profiles"
)

var errSubscriptionProxyUnavailable = errors.New("订阅代理不可用")

type SubscriptionProxyStatus struct {
	Installed    bool   `json:"installed"`
	Running      bool   `json:"running"`
	Healthy      bool   `json:"healthy"`
	Version      string `json:"version,omitempty"`
	State        string `json:"state"`
	PID          int    `json:"pid"`
	ConfigPath   string `json:"config_path,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	APIKeyMasked string `json:"api_key_masked,omitempty"`
}

type SubscriptionProxyLogin struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	Status          string `json:"status"`
	VerificationURL string `json:"verification_url,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	StatusMessage   string `json:"status_message,omitempty"`
}

type SubscriptionProxyAccount struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Label         string `json:"label,omitempty"`
	Email         string `json:"email,omitempty"`
	Status        string `json:"status"`
	StatusMessage string `json:"status_message,omitempty"`
	Disabled      bool   `json:"disabled"`
	Unavailable   bool   `json:"unavailable"`
}

type SubscriptionProxyModel struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

type SubscriptionProxyCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type SubscriptionProxy interface {
	Status(context.Context) (SubscriptionProxyStatus, error)
	ServiceAction(context.Context, string) error
	StartLogin(context.Context, string) (SubscriptionProxyLogin, error)
	Login(context.Context, string) (SubscriptionProxyLogin, error)
	CancelLogin(context.Context, string) error
	Accounts(context.Context) ([]SubscriptionProxyAccount, error)
	UpdateAccount(context.Context, string, string, bool) (SubscriptionProxyAccount, error)
	DeleteAccount(context.Context, string) error
	Models(context.Context) ([]SubscriptionProxyModel, error)
	InferenceKey(context.Context) (string, error)
	Diagnostics(context.Context) ([]SubscriptionProxyCheck, error)
}

type BrowserOpener interface {
	Open(string) error
}

type subscriptionModelSelectionStore interface {
	SelectedModels() ([]SubscriptionProxyModel, error)
	SetSelectedModels([]SubscriptionProxyModel) error
}

type subscriptionProxySelection struct {
	mu       sync.Mutex
	selected map[string]bool
	sessions map[string]string
}

func (s *Server) subscriptionState() *subscriptionProxySelection {
	s.subscriptionProxyStateOnce.Do(func() {
		if s.subscriptionProxyState == nil {
			s.subscriptionProxyState = &subscriptionProxySelection{selected: map[string]bool{}, sessions: map[string]string{}}
		}
	})
	return s.subscriptionProxyState
}

func (s *Server) handleSubscriptionProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.SubscriptionProxy == nil {
		subscriptionProxyError(w, errSubscriptionProxyUnavailable, http.StatusServiceUnavailable)
		return
	}
	status, err := s.SubscriptionProxy.Status(r.Context())
	if err != nil {
		subscriptionProxyError(w, err, http.StatusBadGateway)
		return
	}
	// Management endpoints exist only while CLIProxyAPI is healthy. Stopping or
	// restarting the service must still leave this status endpoint usable rather
	// than turning a successful lifecycle action into a follow-up UI failure.
	if !status.Running || !status.Healthy {
		writeJSON(w, map[string]any{"service": status, "accounts": []SubscriptionProxyAccount{}, "models": []map[string]any{}})
		return
	}
	accounts, err := s.SubscriptionProxy.Accounts(r.Context())
	if err != nil {
		subscriptionProxyError(w, err, http.StatusBadGateway)
		return
	}
	models, err := s.SubscriptionProxy.Models(r.Context())
	if err != nil {
		subscriptionProxyError(w, err, http.StatusBadGateway)
		return
	}
	providerProfiles := map[string]string{}
	if s.Profiles != nil {
		if profileList, listErr := s.Profiles.List(); listErr == nil {
			for _, profile := range profileList {
				if strings.HasPrefix(profile.Source, "subscription-proxy:") {
					providerProfiles[strings.TrimPrefix(profile.Source, "subscription-proxy:")] = profile.ID
				}
			}
		}
	}
	state := s.subscriptionState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if store, ok := s.SubscriptionProxy.(subscriptionModelSelectionStore); ok && len(state.selected) == 0 {
		if persisted, loadErr := store.SelectedModels(); loadErr == nil {
			for _, model := range persisted {
				state.selected[canonicalProvider(model.Provider)+"\x00"+model.ID] = true
			}
		}
	}
	outModels := make([]map[string]any, 0, len(models))
	for _, model := range models {
		outModels = append(outModels, map[string]any{"id": model.ID, "provider": canonicalProvider(model.Provider), "label": model.Label, "selected": state.selected[model.Provider+"\x00"+model.ID] || state.selected[canonicalProvider(model.Provider)+"\x00"+model.ID]})
	}
	writeJSON(w, map[string]any{"service": status, "accounts": accounts, "models": outModels, "providers": providerProfiles})
}

func (s *Server) handleSubscriptionProxyService(w http.ResponseWriter, r *http.Request) {
	if !subscriptionMutation(w, r) || s.requireSubscriptionProxy(w) == nil {
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if !decodeSubscriptionJSON(w, r, &req) {
		return
	}
	if req.Action != "start" && req.Action != "stop" && req.Action != "restart" {
		subscriptionProxyError(w, fmt.Errorf("无效服务操作"), http.StatusBadRequest)
		return
	}
	if err := s.SubscriptionProxy.ServiceAction(r.Context(), req.Action); err != nil {
		subscriptionProxyError(w, err, http.StatusBadGateway)
		return
	}
	status, err := s.SubscriptionProxy.Status(r.Context())
	if err != nil {
		subscriptionProxyError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "service": status})
}

func (s *Server) handleSubscriptionProxyLogin(w http.ResponseWriter, r *http.Request) {
	if !subscriptionLoopback(w, r) || s.requireSubscriptionProxy(w) == nil {
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Provider string `json:"provider"`
		}
		if !decodeSubscriptionJSON(w, r, &req) {
			return
		}
		provider, ok := runtimeProvider(req.Provider)
		if !ok {
			subscriptionProxyError(w, fmt.Errorf("无效登录供应商"), http.StatusBadRequest)
			return
		}
		login, err := s.SubscriptionProxy.StartLogin(r.Context(), provider)
		if err != nil {
			subscriptionProxyError(w, err, http.StatusBadGateway)
			return
		}
		login.Provider = canonicalProvider(login.Provider)
		if !validVerificationURL(login.VerificationURL) {
			subscriptionProxyError(w, fmt.Errorf("登录地址无效"), http.StatusBadGateway)
			return
		}
		state := s.subscriptionState()
		state.mu.Lock()
		state.sessions[login.ID] = login.VerificationURL
		state.mu.Unlock()
		// Auto-open browser so users do not need device codes or manual paste.
		if s.BrowserOpener != nil {
			if openErr := s.BrowserOpener.Open(login.VerificationURL); openErr == nil {
				if login.Status == "" || login.Status == "pending" {
					login.StatusMessage = "已打开浏览器，请用目标 ChatGPT 账号完成授权"
				}
			} else if login.StatusMessage == "" {
				login.StatusMessage = "请点击「打开验证页」在浏览器中完成授权"
			}
		}
		writeJSONStatus(w, login, http.StatusCreated)
	case http.MethodGet:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			subscriptionProxyError(w, fmt.Errorf("缺少登录会话"), http.StatusBadRequest)
			return
		}
		login, err := s.SubscriptionProxy.Login(r.Context(), id)
		if err != nil {
			subscriptionProxyError(w, err, http.StatusBadGateway)
			return
		}
		login.Provider = canonicalProvider(login.Provider)
		writeJSON(w, login)
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			var req struct {
				ID string `json:"id"`
			}
			if !decodeSubscriptionJSON(w, r, &req) {
				return
			}
			id = strings.TrimSpace(req.ID)
		}
		if id == "" {
			subscriptionProxyError(w, fmt.Errorf("缺少登录会话"), http.StatusBadRequest)
			return
		}
		if err := s.SubscriptionProxy.CancelLogin(r.Context(), id); err != nil {
			subscriptionProxyError(w, err, http.StatusBadGateway)
			return
		}
		state := s.subscriptionState()
		state.mu.Lock()
		delete(state.sessions, id)
		state.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleSubscriptionProxyLoginOpen(w http.ResponseWriter, r *http.Request) {
	if !subscriptionMutation(w, r) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decodeSubscriptionJSON(w, r, &req) {
		return
	}
	state := s.subscriptionState()
	state.mu.Lock()
	target := state.sessions[req.ID]
	state.mu.Unlock()
	if target == "" || s.BrowserOpener == nil {
		subscriptionProxyError(w, fmt.Errorf("登录会话不存在"), http.StatusNotFound)
		return
	}
	if err := s.BrowserOpener.Open(target); err != nil {
		subscriptionProxyError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSubscriptionProxyAccount(w http.ResponseWriter, r *http.Request) {
	if !subscriptionLoopback(w, r) || s.requireSubscriptionProxy(w) == nil {
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/subscription-proxy/accounts/"), "/")
	if id == "" || strings.Contains(id, "/") {
		subscriptionProxyError(w, fmt.Errorf("无效账号"), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var req struct {
			Label    string `json:"label"`
			Disabled bool   `json:"disabled"`
		}
		if !decodeSubscriptionJSON(w, r, &req) {
			return
		}
		account, err := s.SubscriptionProxy.UpdateAccount(r.Context(), id, req.Label, req.Disabled)
		if err != nil {
			subscriptionProxyError(w, err, http.StatusBadGateway)
			return
		}
		writeJSON(w, account)
	case http.MethodDelete:
		if err := s.SubscriptionProxy.DeleteAccount(r.Context(), id); err != nil {
			subscriptionProxyError(w, err, http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleSubscriptionProxyModels(w http.ResponseWriter, r *http.Request) {
	if !subscriptionMutation(w, r) || s.requireSubscriptionProxy(w) == nil {
		return
	}
	var req struct {
		Models []struct{ ID, Provider string } `json:"models"`
	}
	if !decodeSubscriptionJSON(w, r, &req) {
		return
	}
	available, err := s.SubscriptionProxy.Models(r.Context())
	if err != nil {
		subscriptionProxyError(w, err, http.StatusBadGateway)
		return
	}
	allowed := map[string]bool{}
	for _, model := range available {
		allowed[canonicalProvider(model.Provider)+"\x00"+model.ID] = true
	}
	selected := map[string]bool{}
	for _, model := range req.Models {
		provider := canonicalProvider(model.Provider)
		key := provider + "\x00" + strings.TrimSpace(model.ID)
		if !allowed[key] {
			subscriptionProxyError(w, fmt.Errorf("模型不在可用列表"), http.StatusBadRequest)
			return
		}
		selected[key] = true
	}
	if store, ok := s.SubscriptionProxy.(subscriptionModelSelectionStore); ok {
		persisted := make([]SubscriptionProxyModel, 0, len(req.Models))
		for _, model := range req.Models {
			persisted = append(persisted, SubscriptionProxyModel{ID: strings.TrimSpace(model.ID), Provider: canonicalProvider(model.Provider), Label: strings.TrimSpace(model.ID)})
		}
		if err := store.SetSelectedModels(persisted); err != nil {
			subscriptionProxyError(w, err, http.StatusInternalServerError)
			return
		}
	}
	state := s.subscriptionState()
	state.mu.Lock()
	state.selected = selected
	state.mu.Unlock()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSubscriptionProxyProviders(w http.ResponseWriter, r *http.Request) {
	if !subscriptionMutation(w, r) || s.requireSubscriptionProxy(w) == nil {
		return
	}
	if s.Profiles == nil {
		subscriptionProxyError(w, fmt.Errorf("Profile 存储不可用"), http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Provider string `json:"provider"`
	}
	if !decodeSubscriptionJSON(w, r, &req) {
		return
	}
	provider := canonicalProvider(req.Provider)
	if provider != "codex" && provider != "gemini" && provider != "grok" {
		subscriptionProxyError(w, fmt.Errorf("无效供应商类型"), http.StatusBadRequest)
		return
	}
	key, err := s.SubscriptionProxy.InferenceKey(r.Context())
	if err != nil || key == "" {
		subscriptionProxyError(w, errSubscriptionProxyUnavailable, http.StatusBadGateway)
		return
	}
	accounts, err := s.SubscriptionProxy.Accounts(r.Context())
	if err != nil {
		subscriptionProxyError(w, err, http.StatusBadGateway)
		return
	}
	models, err := s.SubscriptionProxy.Models(r.Context())
	if err != nil {
		subscriptionProxyError(w, err, http.StatusBadGateway)
		return
	}
	state := s.subscriptionState()
	state.mu.Lock()
	if store, ok := s.SubscriptionProxy.(subscriptionModelSelectionStore); ok && len(state.selected) == 0 {
		if persisted, loadErr := store.SelectedModels(); loadErr == nil {
			for _, model := range persisted {
				state.selected[canonicalProvider(model.Provider)+"\x00"+model.ID] = true
			}
		}
	}
	selectedModels := make([]SubscriptionProxyModel, 0, len(models))
	for _, model := range models {
		if state.selected[canonicalProvider(model.Provider)+"\x00"+model.ID] {
			selectedModels = append(selectedModels, model)
		}
	}
	state.mu.Unlock()
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	list, err := s.Profiles.List()
	if err != nil {
		subscriptionProxyError(w, err, http.StatusInternalServerError)
		return
	}
	created := make([]profiles.Profile, 0, 1)
	createdNew := make([]string, 0, 1)
	updatedPrevious := make([]profiles.Profile, 0, 1)
	baseURL := s.SubscriptionProxyBaseURL()
	providerName := map[string]string{"codex": "订阅代理 · ChatGPT/Codex", "gemini": "订阅代理 · Google Gemini", "grok": "订阅代理 · Grok Build"}[provider]
	for _, def := range []struct{ provider, name string }{{provider, providerName}} {
		profile := subscriptionProfile(def.provider, def.name, key, accounts, selectedModels, baseURL)
		if len(profile.Models) == 0 {
			subscriptionProxyError(w, fmt.Errorf("请先添加并启用 %s 订阅账号，然后保存至少一个该类型的模型", subscriptionProviderLabel(provider)), http.StatusBadRequest)
			return
		}
		var stored *profiles.Profile
		for i := range list {
			if list[i].Source == "subscription-proxy:"+def.provider {
				stored = &list[i]
				break
			}
		}
		if stored == nil {
			profile, err = s.Profiles.Create(profile)
			if err == nil {
				createdNew = append(createdNew, profile.ID)
			}
		} else {
			previous := *stored
			profile, err = s.Profiles.Update(stored.ID, profile)
			if err == nil {
				updatedPrevious = append(updatedPrevious, previous)
			}
		}
		if err != nil {
			rollbackSubscriptionProfiles(s.Profiles, createdNew, updatedPrevious)
			subscriptionProxyError(w, err, http.StatusInternalServerError)
			return
		}
		created = append(created, profile)
	}
	if s.Routing != nil {
		if err := s.applyCurrentRoutingLocked(); err != nil {
			rollbackSubscriptionProfiles(s.Profiles, createdNew, updatedPrevious)
			subscriptionProxyError(w, err, http.StatusInternalServerError)
			return
		}
	}
	s.changed()
	writeJSON(w, map[string]any{"providers": created})
}

func rollbackSubscriptionProfiles(store *profiles.Store, createdIDs []string, updatedPrevious []profiles.Profile) {
	for i := len(createdIDs) - 1; i >= 0; i-- {
		_ = store.Delete(createdIDs[i])
	}
	for i := len(updatedPrevious) - 1; i >= 0; i-- {
		previous := updatedPrevious[i]
		_, _ = store.Update(previous.ID, previous)
	}
}

func subscriptionProviderLabel(provider string) string {
	switch provider {
	case "codex":
		return "Codex/ChatGPT"
	case "gemini":
		return "Gemini"
	case "grok":
		return "Grok"
	default:
		return provider
	}
}

func subscriptionProfile(provider, name, key string, accounts []SubscriptionProxyAccount, models []SubscriptionProxyModel, baseURL string) profiles.Profile {
	p := profiles.Profile{Name: name, Source: "subscription-proxy:" + provider, UpstreamFormat: "openai_chat", BaseURL: baseURL, APIKey: key, DefaultReasoningEffort: "low"}
	hasAccount := false
	for _, account := range accounts {
		if canonicalProvider(account.Provider) == provider && !account.Disabled && !account.Unavailable {
			hasAccount = true
			break
		}
	}
	if !hasAccount {
		return p
	}
	for _, model := range models {
		if canonicalProvider(model.Provider) != provider {
			continue
		}
		p.AvailableModels = append(p.AvailableModels, model.ID)
		p.Models = append(p.Models, profiles.ModelDef{Name: model.Label, Model: model.ID, BaseURL: baseURL, APIKey: key, APIBackend: "chat_completions", ReasoningEffortsSource: "default"})
	}
	return p
}

func (s *Server) handleSubscriptionProxyDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !subscriptionMutation(w, r) || s.requireSubscriptionProxy(w) == nil {
		return
	}
	checks, err := s.SubscriptionProxy.Diagnostics(r.Context())
	if err != nil {
		subscriptionProxyError(w, err, http.StatusBadGateway)
		return
	}
	for i := range checks {
		checks[i].Message = redactDiagnostic(checks[i].Message)
	}
	writeJSON(w, map[string]any{"checks": checks})
}

func (s *Server) requireSubscriptionProxy(w http.ResponseWriter) SubscriptionProxy {
	if s.SubscriptionProxy == nil {
		subscriptionProxyError(w, errSubscriptionProxyUnavailable, http.StatusServiceUnavailable)
	}
	return s.SubscriptionProxy
}

func subscriptionMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return false
	}
	return subscriptionLoopback(w, r)
}

func subscriptionLoopback(w http.ResponseWriter, r *http.Request) bool {
	if !isLoopbackRequest(r) {
		subscriptionProxyError(w, fmt.Errorf("仅允许本机操作"), http.StatusForbidden)
		return false
	}
	return true
}

func decodeSubscriptionJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := httpjson.Decode(w, r, out, httpjson.Options{MaxBytes: 32 << 10}); err != nil {
		subscriptionProxyError(w, fmt.Errorf("请求格式无效"), http.StatusBadRequest)
		return false
	}
	return true
}

func runtimeProvider(provider string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return "codex", true
	case "gemini":
		return "antigravity", true
	case "grok":
		return "xai", true
	default:
		return "", false
	}
}

func canonicalProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "antigravity", "gemini":
		return "gemini"
	case "xai", "grok":
		return "grok"
	case "codex":
		return "codex"
	default:
		return ""
	}
}

func validVerificationURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

func redactDiagnostic(message string) string {
	if strings.Contains(message, "/") || strings.Contains(message, "\\") || strings.Contains(strings.ToLower(message), "key") || strings.Contains(strings.ToLower(message), "token") || len(message) > 160 {
		return "详细信息已脱敏"
	}
	return message
}

func subscriptionProxyError(w http.ResponseWriter, err error, status int) {
	message := "订阅代理操作失败"
	if status >= 400 && status < 500 && err != nil {
		message = err.Error()
	} else if errors.Is(err, os.ErrNotExist) {
		message = "资源不存在"
	}
	writeJSONStatus(w, map[string]string{"error": message}, status)
}
