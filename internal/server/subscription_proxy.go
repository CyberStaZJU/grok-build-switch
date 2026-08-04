package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"grok_switch/internal/httpjson"
	"grok_switch/internal/modelvariants"
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

type subscriptionModelReconciler interface {
	ReconcileModels(context.Context) ([]SubscriptionProxyModel, error)
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
	escapedTail := strings.TrimPrefix(r.URL.EscapedPath(), "/api/subscription-proxy/accounts/")
	lowerEscapedTail := strings.ToLower(escapedTail)
	if strings.Contains(lowerEscapedTail, "%2f") || strings.Contains(lowerEscapedTail, "%5c") || strings.Contains(lowerEscapedTail, "%00") {
		subscriptionProxyError(w, fmt.Errorf("无效账号"), http.StatusBadRequest)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/subscription-proxy/accounts/"), "/")
	if err := validateSubscriptionAccountID(id); err != nil {
		subscriptionProxyError(w, err, http.StatusBadRequest)
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

func validateSubscriptionAccountID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsRune(id, '\x00') || strings.ContainsAny(id, `/\\`) || filepath.Base(id) != id || filepath.Clean(id) != id || filepath.IsAbs(id) || filepath.VolumeName(id) != "" {
		return fmt.Errorf("无效账号")
	}
	return nil
}

func reconcileSubscriptionModels(ctx context.Context, proxy SubscriptionProxy) ([]SubscriptionProxyModel, error) {
	if reconciler, ok := proxy.(subscriptionModelReconciler); ok {
		return reconciler.ReconcileModels(ctx)
	}
	return proxy.Models(ctx)
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
	available, err := reconcileSubscriptionModels(r.Context(), s.SubscriptionProxy)
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
	models, err := reconcileSubscriptionModels(r.Context(), s.SubscriptionProxy)
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
		stored, findErr := findSubscriptionProfile(list, def.provider, def.name, baseURL)
		if findErr != nil {
			subscriptionProxyError(w, findErr, http.StatusConflict)
			return
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
			rollbackErr := rollbackSubscriptionProfiles(s.Profiles, createdNew, updatedPrevious)
			subscriptionProxyError(w, transactionRollbackError(err, rollbackErr), http.StatusInternalServerError)
			return
		}
		created = append(created, profile)
	}
	if s.Routing != nil {
		if err := s.applyCurrentRoutingLocked(); err != nil {
			rollbackErr := rollbackSubscriptionProfiles(s.Profiles, createdNew, updatedPrevious)
			subscriptionProxyError(w, transactionRollbackError(err, rollbackErr), http.StatusInternalServerError)
			return
		}
	}
	s.changed()
	writeJSON(w, map[string]any{"providers": created})
}

func rollbackSubscriptionProfiles(store *profiles.Store, createdIDs []string, updatedPrevious []profiles.Profile) error {
	var rollbackErrs []error
	for i := len(createdIDs) - 1; i >= 0; i-- {
		if err := store.Delete(createdIDs[i]); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("删除新建订阅配置 %q 失败: %w", createdIDs[i], err))
		}
	}
	for i := len(updatedPrevious) - 1; i >= 0; i-- {
		previous := updatedPrevious[i]
		if err := store.Restore(previous); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("恢复订阅配置 %q 失败: %w", previous.ID, err))
		}
	}
	return errors.Join(rollbackErrs...)
}

func transactionRollbackError(cause, rollbackErr error) error {
	if rollbackErr == nil {
		return cause
	}
	return errors.Join(cause, fmt.Errorf("回滚未完整完成: %w", rollbackErr))
}

// findSubscriptionProfile first prefers the current server-owned identity. For
// upgrades from builds that predate Profile.Source, it may adopt one exact,
// unambiguous legacy subscription profile instead of creating a duplicate.
// Multiple legacy matches fail closed so ordinary user Profiles are never
// silently claimed by the subscription proxy.
func findSubscriptionProfile(list []profiles.Profile, provider, name, baseURL string) (*profiles.Profile, error) {
	provider = canonicalProvider(provider)
	source := "subscription-proxy:" + provider
	for i := range list {
		if list[i].Source == source {
			return &list[i], nil
		}
	}
	var legacy *profiles.Profile
	for i := range list {
		if !isLegacySubscriptionProfile(list[i], provider, name, baseURL) {
			continue
		}
		if legacy != nil {
			return nil, fmt.Errorf("检测到多个未标记的旧 %s 订阅供应商；请先人工确认", subscriptionProviderLabel(provider))
		}
		legacy = &list[i]
	}
	return legacy, nil
}

func isLegacySubscriptionProfile(profile profiles.Profile, provider, name, baseURL string) bool {
	if profile.Source != "" || profile.Name != name || strings.TrimRight(profile.BaseURL, "/") != strings.TrimRight(baseURL, "/") || len(profile.Models) == 0 {
		return false
	}
	prefix := "subscription/" + canonicalProvider(provider) + "/"
	for _, model := range profile.Models {
		alias := strings.TrimSpace(model.Name)
		if alias == "" {
			alias = strings.TrimSpace(model.Model)
		}
		if !strings.HasPrefix(alias, prefix) {
			return false
		}
	}
	return true
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
	provider = canonicalProvider(provider)
	p := profiles.Profile{
		Name: name, Source: "subscription-proxy:" + provider, UpstreamFormat: "openai_chat",
		BaseURL: baseURL, APIKey: key, DefaultReasoningEffort: "none",
	}
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
	seen := map[string]bool{}
	for _, model := range models {
		if canonicalProvider(model.Provider) != provider {
			continue
		}
		alias := strings.TrimSpace(model.ID)
		if alias == "" || seen[alias] {
			continue
		}
		if provider == "codex" {
			if _, generated := modelvariants.TrustedCodexPhysicalFromFastAlias(alias); generated {
				// Fast aliases are generated by Switch from their exact Standard
				// anchor. They are not selectable physical catalog entries.
				continue
			}
			if physicalID, trusted := modelvariants.TrustedCodexPhysicalFromStandardAlias(alias); trusted {
				standard, _ := modelvariants.CodexStandardAlias(physicalID)
				fast, _ := modelvariants.CodexFastAlias(physicalID)
				efforts := modelvariants.TrustedCodexReasoningEfforts()
				p.AvailableModels = append(p.AvailableModels, standard, fast)
				p.Models = append(p.Models,
					trustedSubscriptionModel(standard, standard, baseURL, key, profiles.SpeedTierStandard, standard, efforts),
					trustedSubscriptionModel(fast, fast, baseURL, key, profiles.SpeedTierFast, standard, efforts),
				)
				seen[standard], seen[fast] = true, true
				if p.DefaultModel == "" {
					p.DefaultModel = standard
					p.DefaultReasoningEffort = "low"
				}
				continue
			}
		}
		seen[alias] = true
		p.AvailableModels = append(p.AvailableModels, alias)
		p.Models = append(p.Models, profiles.ModelDef{
			Name: alias, Model: alias, BaseURL: baseURL, APIKey: key,
			APIBackend: "chat_completions", ReasoningEffortsSource: "default",
		})
		if p.DefaultModel == "" {
			p.DefaultModel = alias
		}
	}
	return p
}

func trustedSubscriptionModel(name, model, baseURL, key, tier, anchor string, efforts []string) profiles.ModelDef {
	return profiles.ModelDef{
		Name: name, Model: model, BaseURL: baseURL, APIKey: key, APIBackend: "chat_completions",
		SupportsReasoningEffort: true, ReasoningEfforts: append([]string(nil), efforts...), ReasoningEffortsSource: "declared",
		SpeedTier: tier, StandardAnchor: anchor,
	}
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
	if status >= 500 && err != nil {
		log.Printf("subscription proxy request failed (status=%d): %v", status, err)
	}
	if status >= 400 && status < 500 && err != nil {
		message = err.Error()
	} else if errors.Is(err, os.ErrNotExist) {
		message = "资源不存在"
	}
	writeJSONStatus(w, map[string]string{"error": message}, status)
}
