package server

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"

	"grok_switch/internal/autostart"
	"grok_switch/internal/collaboration"
	grokconfig "grok_switch/internal/config"
	"grok_switch/internal/httpjson"
	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/remoteaccess"
	"grok_switch/internal/routing"
	"grok_switch/internal/settings"
	"grok_switch/internal/ssh"
	"grok_switch/internal/switcher"
)

type Server struct {
	Paths                      paths.Paths
	Profiles                   *profiles.Store
	Routing                    *routing.Store
	Collaboration              *collaboration.Store
	Settings                   *settings.Store
	RemoteAccess               *remoteaccess.Store
	Switcher                   *switcher.Switcher
	SubscriptionProxy          SubscriptionProxy
	SSH                        *ssh.Handler
	BrowserOpener              BrowserOpener
	Assets                     embed.FS
	ExePath                    string
	ActualPort                 int
	onChanged                  func()
	listenerMu                 sync.Mutex
	settingsMu                 sync.Mutex
	listener                   net.Listener
	bindHost                   string
	httpServer                 *http.Server
	loginMu                    sync.Mutex
	loginFails                 map[string]loginFailure
	subscriptionProxyState     *subscriptionProxySelection
	subscriptionProxyStateOnce sync.Once
	routingMu                  sync.Mutex
	collaborationMu            sync.Mutex
	csrfMu                     sync.Mutex
	csrfSecret                 string
	reconfigureLAN             func(bool) error
	autostartSync              func(bool, string, bool) error
	resetRemoteSessions        func() error
	restoreRemoteSessions      func(remoteaccess.Snapshot) error
	updateSettings             func(settings.Settings) (settings.Settings, error)
	persistCollaboration       func(collaboration.Policy) (collaboration.Policy, error)
}

func (s *Server) SetOnChanged(fn func()) {
	s.onChanged = fn
}

func (s *Server) Listen(preferred int) (*http.Server, int, error) {
	if err := settings.ValidatePort(preferred); err != nil {
		return nil, 0, err
	}
	mux := http.NewServeMux()
	s.routes(mux)
	var listener net.Listener
	var err error
	port := preferred
	currentSettings := settings.Default()
	if s.Settings != nil {
		currentSettings, err = s.Settings.Get()
		if err != nil {
			return nil, 0, err
		}
	}
	bindHost := s.bindHostFor(currentSettings.LANAccessEnabled)
	for i := 0; i < 20 && port <= settings.MaxPort; i++ {
		listener, err = net.Listen("tcp", bindHost+":"+strconv.Itoa(port))
		if err == nil {
			break
		}
		port++
	}
	if listener == nil {
		listener, err = net.Listen("tcp", bindHost+":0")
		if err == nil {
			if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
				port = tcpAddr.Port
			}
		}
	}
	if listener == nil {
		return nil, 0, err
	}
	s.ActualPort = port
	if s.Settings != nil {
		if err := s.Settings.SetActualPort(port); err != nil {
			listener.Close()
			return nil, 0, err
		}
	}
	srv := newApplicationHTTPServer(s.withAccess(mux))
	s.listenerMu.Lock()
	s.listener = listener
	s.bindHost = bindHost
	s.httpServer = srv
	s.listenerMu.Unlock()
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			fmt.Fprintf(os.Stderr, "http server: %v\n", err)
		}
	}()
	return srv, port, nil
}

func newApplicationHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ErrorLog:          log.New(os.Stderr, "http: ", log.LstdFlags),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      15 * time.Minute,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    256 << 10,
	}
}

func (s *Server) reconfigureLANAccess(enabled bool) error {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	desired := s.bindHostFor(enabled)
	if s.listener == nil || s.bindHost == desired {
		return nil
	}
	oldListener := s.listener
	oldHost := s.bindHost
	_ = oldListener.Close()
	listener, err := net.Listen("tcp", net.JoinHostPort(desired, strconv.Itoa(s.ActualPort)))
	if err != nil {
		restored, restoreErr := net.Listen("tcp", net.JoinHostPort(oldHost, strconv.Itoa(s.ActualPort)))
		if restoreErr == nil {
			s.listener = restored
			go s.serveListenerLocked(restored)
		}
		return err
	}
	s.listener = listener
	s.bindHost = desired
	go s.serveListenerLocked(listener)
	return nil
}

func (s *Server) serveListenerLocked(listener net.Listener) {
	if s.httpServer == nil {
		return
	}
	if err := s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		fmt.Fprintf(os.Stderr, "http server reconfigure: %v\n", err)
	}
}

func (s *Server) Shutdown(ctx context.Context, srv *http.Server) error {
	return srv.Shutdown(ctx)
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("/pair", s.handlePair)
	mux.HandleFunc("/api/lan-access", s.handleLANAccess)
	mux.HandleFunc("/api/csrf", s.handleCSRF)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/routing", s.handleRouting)
	mux.HandleFunc("/api/routing/policy", s.handleRoutingPolicy)
	mux.HandleFunc("/api/routing/reapply", s.handleRoutingReapply)
	mux.HandleFunc("/api/collaboration", s.handleCollaboration)
	mux.HandleFunc("/api/collaboration/spec", s.handleCollaborationSpec)
	mux.HandleFunc("/api/collaboration/preview", s.handleCollaborationPreview)
	mux.HandleFunc("/api/cache-stats", s.handleCacheStats)
	mux.HandleFunc("/api/profiles", s.handleProfiles)
	mux.HandleFunc("/api/profiles/", s.handleProfileByID)
	mux.HandleFunc("/api/official/activate", s.handleOfficialActivate)
	mux.HandleFunc("/api/import", s.handleImport)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/models/fetch", s.handleFetchModels)
	mux.HandleFunc("/api/models/reasoning-efforts", s.handleReasoningEfforts)
	mux.HandleFunc("/api/connection/test", s.handleConnectionTest)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/config/preview", s.handleConfigPreview)
	mux.HandleFunc("/api/config/privacy", s.handleConfigPrivacy)
	mux.HandleFunc("/api/subscription-proxy", s.handleSubscriptionProxy)
	mux.HandleFunc("/api/subscription-proxy/service", s.handleSubscriptionProxyService)
	mux.HandleFunc("/api/subscription-proxy/login", s.handleSubscriptionProxyLogin)
	mux.HandleFunc("/api/subscription-proxy/login/open", s.handleSubscriptionProxyLoginOpen)
	mux.HandleFunc("/api/subscription-proxy/accounts/", s.handleSubscriptionProxyAccount)
	mux.HandleFunc("/api/subscription-proxy/models", s.handleSubscriptionProxyModels)
	mux.HandleFunc("/api/subscription-proxy/providers", s.handleSubscriptionProxyProviders)
	mux.HandleFunc("/api/subscription-proxy/diagnostics", s.handleSubscriptionProxyDiagnostics)
	mux.HandleFunc("/subscription-proxy/v1", s.handleSubscriptionInference)
	mux.HandleFunc("/subscription-proxy/v1/", s.handleSubscriptionInference)
	if s.SSH != nil {
		s.SSH.RegisterRoutes(mux)
	}
	mux.HandleFunc("/", s.handleStatic)
}

func (s *Server) bindHostFor(enabled bool) string {
	if enabled {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	active, matches, err := s.Switcher.ActiveStatus()
	if err != nil && !os.IsNotExist(err) {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	var activeRouting any
	routingMatches := false
	if s.Routing != nil {
		dto, hydrated, routingErr := s.currentRouting()
		if routingErr != nil && !os.IsNotExist(routingErr) {
			writeError(w, routingErr, http.StatusInternalServerError)
			return
		}
		if routingErr == nil {
			activeRouting = dto
			routingMatches, routingErr = grokconfig.CurrentMatchesRouting(s.Paths.GrokConfig, hydrated)
			if routingErr != nil && !os.IsNotExist(routingErr) {
				writeError(w, routingErr, http.StatusInternalServerError)
				return
			}
			if routingMatches {
				matches = true
			}
		}
	}
	currentSettings, _ := s.Settings.Get()
	officialLoggedIn := s.officialLoggedIn()
	activeSafe := active
	activeSafe.APIKey = ""
	for i := range activeSafe.Models {
		activeSafe.Models[i].APIKey = ""
		activeSafe.Models[i].ExtraHeaders = nil
	}
	var activeProfileResponse any = activeSafe
	configPath := s.Paths.GrokConfig
	dataDir := s.Paths.DataDir
	if !isLoopbackRequest(r) {
		activeProfileResponse = publicProfile(activeSafe)
		configPath = ""
		dataDir = ""
	}
	officialActive := false
	if s.Routing != nil {
		if stored, storedErr := s.Routing.Snapshot(); storedErr == nil {
			officialActive = stored.IsOfficial()
		}
	}
	// Build routing policy display fields for menu bar.
	activeID := active.ID
	if s.Routing != nil {
		if stored, storedErr := s.Routing.Snapshot(); storedErr == nil {
			activeID = stored.ActiveProviderID
		}
	}
	var defaultModel, webSearchModel, exploreModel, planModel string
	if s.Routing != nil {
		if stored, storedErr := s.Routing.Snapshot(); storedErr == nil {
			policy := stored.ActivePolicy()
			if stored.IsOfficial() {
				defaultModel = policy.Default
				webSearchModel = policy.WebSearch
				exploreModel = policy.Subagents.Explore
				planModel = policy.Subagents.Plan
			} else {
				if route, ok := stored.Route(policy.Default); ok {
					defaultModel = route.Name
				}
				if route, ok := stored.Route(policy.WebSearch); ok {
					webSearchModel = route.Name
				}
				if route, ok := stored.Route(policy.Subagents.Explore); ok {
					exploreModel = route.Name
				}
				if route, ok := stored.Route(policy.Subagents.Plan); ok {
					planModel = route.Name
				}
			}
		}
	}
	writeJSON(w, map[string]any{
		"active_profile":         activeProfileResponse,
		"active_routing":         activeRouting,
		"official_active":        officialActive,
		"official_logged_in":     officialLoggedIn,
		"config_path":            configPath,
		"data_dir":               dataDir,
		"port":                   s.ActualPort,
		"settings":               currentSettings,
		"config_matches_active":  matches,
		"config_matches_routing": routingMatches,
		"active_id":              activeID,
		"default_model":          defaultModel,
		"web_search_model":       webSearchModel,
		"explore_model":          exploreModel,
		"plan_model":             planModel,
	})
}

var startGrokLogin = func() error {
	return exec.Command("grok", "login").Start()
}

func (s *Server) handleOfficialActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	policy := routing.RoutingPolicy{Official: true, Default: defaultOfficialRoutingModels[0].Name}
	if s.Routing != nil {
		if stored, storedErr := s.Routing.Snapshot(); storedErr == nil {
			candidate := stored.ProviderPolicies[routing.OfficialProviderID]
			candidate.Official = true
			if validateOfficialRoutingPolicy(candidate) == nil {
				policy = candidate
			}
		}
	}
	if err := validateOfficialRoutingPolicy(policy); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if !s.officialLoggedIn() {
		if err := startGrokLogin(); err != nil {
			writeError(w, fmt.Errorf("启动 grok login 失败，未切换配置: %w", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "login_required": true, "switched": false,
			"message": "已启动 Grok 官方登录，登录完成后请再次切换",
		})
		return
	}
	var err error
	if s.Routing != nil && s.Profiles != nil {
		s.routingMu.Lock()
		profileList, listErr := s.Profiles.List()
		if listErr == nil {
			_, listErr = s.applyRoutingPolicyTransaction(profileList, policy)
		}
		s.routingMu.Unlock()
		err = listErr
	} else {
		err = s.Switcher.ActivateOfficial()
	}
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	s.changed()
	writeJSON(w, map[string]any{
		"ok":             true,
		"login_required": false,
		"switched":       true,
		"message":        "已切换到官方账号",
	})
}

type profilePublicModelDTO struct {
	Name                    string   `json:"name"`
	Model                   string   `json:"model"`
	APIBackend              string   `json:"api_backend"`
	HasAPIKey               bool     `json:"has_api_key"`
	SupportsBackendSearch   bool     `json:"supports_backend_search"`
	SupportsReasoningEffort bool     `json:"supports_reasoning_effort"`
	ReasoningEfforts        []string `json:"reasoning_efforts"`
	ReasoningEffortsSource  string   `json:"reasoning_efforts_source,omitempty"`
	ContextWindow           int64    `json:"context_window"`
	MaxCompletionTokens     int64    `json:"max_completion_tokens"`
}

type profilePublicDTO struct {
	ID                     string                  `json:"id"`
	IsActive               bool                    `json:"is_active"`
	Name                   string                  `json:"name"`
	Source                 string                  `json:"source,omitempty"`
	UpstreamFormat         string                  `json:"upstream_format"`
	HasAPIKey              bool                    `json:"has_api_key"`
	AvailableModels        []string                `json:"available_models"`
	DefaultModel           string                  `json:"default_model"`
	DefaultReasoningEffort string                  `json:"default_reasoning_effort"`
	Models                 []profilePublicModelDTO `json:"models"`
	CreatedAt              time.Time               `json:"created_at"`
	UpdatedAt              time.Time               `json:"updated_at"`
}

type profileMutationModelDTO struct {
	Name                    string            `json:"name"`
	Model                   string            `json:"model"`
	BaseURL                 string            `json:"base_url"`
	APIKey                  string            `json:"api_key"`
	APIBackend              string            `json:"api_backend"`
	ExtraHeaders            map[string]string `json:"extra_headers"`
	SupportsBackendSearch   bool              `json:"supports_backend_search"`
	SupportsReasoningEffort bool              `json:"supports_reasoning_effort"`
	ReasoningEfforts        []string          `json:"reasoning_efforts"`
	ReasoningEffortsSource  string            `json:"reasoning_efforts_source,omitempty"`
	ContextWindow           int64             `json:"context_window"`
	MaxCompletionTokens     int64             `json:"max_completion_tokens"`
}

type profileMutationDTO struct {
	ID                     string                    `json:"id,omitempty"`
	Name                   string                    `json:"name"`
	UpstreamFormat         string                    `json:"upstream_format"`
	BaseURL                string                    `json:"base_url"`
	APIKey                 string                    `json:"api_key"`
	AvailableModels        []string                  `json:"available_models"`
	DefaultModel           string                    `json:"default_model"`
	DefaultReasoningEffort string                    `json:"default_reasoning_effort"`
	Models                 []profileMutationModelDTO `json:"models"`
}

type profileLocalDTO struct {
	profileMutationDTO
	IsActive bool `json:"is_active"`
}

func publicProfile(profile profiles.Profile) profilePublicDTO {
	out := profilePublicDTO{
		ID: profile.ID, Name: profile.Name, Source: profile.Source,
		UpstreamFormat: profile.UpstreamFormat,
		HasAPIKey:      profile.EffectiveAPIKey() != "", AvailableModels: append([]string(nil), profile.AvailableModels...),
		DefaultModel: profile.DefaultModel, DefaultReasoningEffort: profile.DefaultReasoningEffort,
		CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt,
		Models: make([]profilePublicModelDTO, len(profile.Models)),
	}
	for i, model := range profile.Models {
		out.Models[i] = profilePublicModelDTO{
			Name: model.Name, Model: model.Model, APIBackend: model.APIBackend,
			HasAPIKey: model.APIKey != "", SupportsBackendSearch: model.SupportsBackendSearch,
			SupportsReasoningEffort: model.SupportsReasoningEffort,
			ReasoningEfforts:        append([]string(nil), model.ReasoningEfforts...), ReasoningEffortsSource: model.ReasoningEffortsSource,
			ContextWindow: model.ContextWindow, MaxCompletionTokens: model.MaxCompletionTokens,
		}
	}
	return out
}

func editableProfile(profile profiles.Profile, active bool) profileLocalDTO {
	out := profileLocalDTO{profileMutationDTO: profileMutationFromProfile(profile), IsActive: active}
	return out
}

func profileMutationFromProfile(profile profiles.Profile) profileMutationDTO {
	out := profileMutationDTO{
		ID: profile.ID, Name: profile.Name, UpstreamFormat: profile.UpstreamFormat,
		BaseURL: profile.BaseURL, APIKey: profile.APIKey,
		AvailableModels: append([]string(nil), profile.AvailableModels...), DefaultModel: profile.DefaultModel,
		DefaultReasoningEffort: profile.DefaultReasoningEffort,
		Models:                 make([]profileMutationModelDTO, len(profile.Models)),
	}
	for i, model := range profile.Models {
		out.Models[i] = profileMutationModelDTO{
			Name: model.Name, Model: model.Model, BaseURL: model.BaseURL, APIKey: model.APIKey,
			APIBackend: model.APIBackend, ExtraHeaders: cloneStringMap(model.ExtraHeaders),
			SupportsBackendSearch: model.SupportsBackendSearch, SupportsReasoningEffort: model.SupportsReasoningEffort,
			ReasoningEfforts: append([]string(nil), model.ReasoningEfforts...), ReasoningEffortsSource: model.ReasoningEffortsSource,
			ContextWindow: model.ContextWindow, MaxCompletionTokens: model.MaxCompletionTokens,
		}
	}
	return out
}

func (dto profileMutationDTO) profile() profiles.Profile {
	profile := profiles.Profile{
		ID: dto.ID, Name: dto.Name, UpstreamFormat: dto.UpstreamFormat, BaseURL: dto.BaseURL, APIKey: dto.APIKey,
		AvailableModels: append([]string(nil), dto.AvailableModels...), DefaultModel: dto.DefaultModel,
		DefaultReasoningEffort: dto.DefaultReasoningEffort, Models: make([]profiles.ModelDef, len(dto.Models)),
	}
	for i, model := range dto.Models {
		profile.Models[i] = profiles.ModelDef{
			Name: model.Name, Model: model.Model, BaseURL: model.BaseURL, APIKey: model.APIKey,
			APIBackend: model.APIBackend, ExtraHeaders: cloneStringMap(model.ExtraHeaders),
			SupportsBackendSearch: model.SupportsBackendSearch, SupportsReasoningEffort: model.SupportsReasoningEffort,
			ReasoningEfforts: append([]string(nil), model.ReasoningEfforts...), ReasoningEffortsSource: model.ReasoningEffortsSource,
			ContextWindow: model.ContextWindow, MaxCompletionTokens: model.MaxCompletionTokens,
		}
	}
	return profile
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

const (
	managementJSONLimit = int64(32 << 10)
	configJSONLimit     = int64(2 << 20)
)

func decodeManagementJSON(w http.ResponseWriter, r *http.Request, out any) error {
	return httpjson.Decode(w, r, out, httpjson.Options{MaxBytes: managementJSONLimit})
}

func decodeConfigJSON(w http.ResponseWriter, r *http.Request, out any) error {
	return httpjson.Decode(w, r, out, httpjson.Options{MaxBytes: configJSONLimit})
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list, err := s.Profiles.List()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		activeProfileID := ""
		if s.Routing != nil {
			if stored, snapshotErr := s.Routing.Snapshot(); snapshotErr == nil {
				if provider, ok := stored.Provider(stored.ActiveProviderID); ok {
					activeProfileID = provider.ProfileID
				}
			}
		}
		public := make([]profilePublicDTO, len(list))
		for i, profile := range list {
			public[i] = publicProfile(profile)
			public[i].IsActive = profile.ID == activeProfileID
		}
		if !isLoopbackRequest(r) {
			writeJSON(w, public)
			return
		}
		local := make([]profileLocalDTO, len(list))
		for i, profile := range list {
			local[i] = editableProfile(profile, profile.ID == activeProfileID)
		}
		writeJSON(w, local)
	case http.MethodPost:
		var request profileMutationDTO
		if err := decodeManagementJSON(w, r, &request); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		profile := request.profile()
		if err := profiles.ValidateEndpoints(profile); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		s.routingMu.Lock()
		created, err := s.Profiles.Create(profile)
		if err == nil && s.Routing != nil {
			err = s.applyCurrentRoutingLocked()
		}
		s.routingMu.Unlock()
		if err != nil {
			if created.ID != "" {
				if rollbackErr := s.Profiles.Delete(created.ID); rollbackErr != nil {
					err = fmt.Errorf("创建 Profile 后重建路由失败: %v；删除新 Profile 失败: %w", err, rollbackErr)
				}
			}
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		s.changed()
		writeJSONStatus(w, created, http.StatusCreated)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleProfileByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, fmt.Errorf("missing profile id"), http.StatusBadRequest)
		return
	}
	id := parts[0]
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var request profileMutationDTO
		if err := decodeManagementJSON(w, r, &request); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		profile := request.profile()
		if err := profiles.ValidateEndpoints(profile); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		s.routingMu.Lock()
		previous, previousErr := s.Profiles.Get(id)
		if previousErr == nil && strings.HasPrefix(previous.Source, "subscription-proxy:") {
			s.routingMu.Unlock()
			writeError(w, fmt.Errorf("订阅代理供应商只能通过订阅代理页面更新"), http.StatusConflict)
			return
		}
		updated, err := s.Profiles.Update(id, profile)
		if err == nil && s.Routing != nil {
			err = s.applyCurrentRoutingLocked()
		}
		if err != nil && previousErr == nil {
			if rollbackErr := s.Profiles.Restore(previous); rollbackErr != nil {
				err = fmt.Errorf("更新 Profile 后重建路由失败: %v；恢复原 Profile 失败: %w", err, rollbackErr)
			}
		}
		s.routingMu.Unlock()
		if err != nil {
			status := http.StatusInternalServerError
			if os.IsNotExist(err) {
				status = http.StatusNotFound
			}
			writeError(w, err, status)
			return
		}
		s.changed()
		writeJSON(w, updated)
	case http.MethodDelete:
		s.routingMu.Lock()
		if s.Routing != nil {
			if stored, snapshotErr := s.Routing.Snapshot(); snapshotErr == nil {
				if provider, ok := stored.Provider(stored.ActiveProviderID); ok && provider.ProfileID == id {
					s.routingMu.Unlock()
					writeError(w, fmt.Errorf("当前启用的供应商不能删除；请先启用另一个供应商"), http.StatusConflict)
					return
				}
			}
		}
		previous, previousErr := s.Profiles.Get(id)
		err := s.Profiles.Delete(id)
		deleted := err == nil
		if deleted && s.Routing != nil {
			err = s.applyCurrentRoutingLocked()
		}
		if err != nil && deleted && previousErr == nil {
			if rollbackErr := s.Profiles.Restore(previous); rollbackErr != nil {
				err = fmt.Errorf("删除 Profile 后重建路由失败: %v；恢复原 Profile 失败: %w", err, rollbackErr)
			}
		}
		s.routingMu.Unlock()
		if err != nil {
			status := http.StatusInternalServerError
			if os.IsNotExist(err) {
				status = http.StatusNotFound
			}
			writeError(w, err, status)
			return
		}
		s.changed()
		writeJSON(w, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Name   string `json:"name"`
		Active bool   `json:"active"`
	}
	if err := decodeManagementJSON(w, r, &req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		req.Name = "Imported"
	}
	profile, err := s.Switcher.ImportCurrent(req.Name, req.Active)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	s.changed()
	writeJSONStatus(w, profile, http.StatusCreated)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		current, err := s.Settings.Get()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, current)
	case http.MethodPut:
		s.settingsMu.Lock()
		defer s.settingsMu.Unlock()
		current, err := s.Settings.Get()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		var next settings.Settings
		if err := decodeManagementJSON(w, r, &next); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		next, err = settings.Prepare(next)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		var remoteSnapshot remoteaccess.Snapshot
		if s.RemoteAccess != nil {
			remoteSnapshot, err = s.RemoteAccess.Get()
			if err != nil {
				writeError(w, err, http.StatusInternalServerError)
				return
			}
		}
		rollback := func(primary error, listenerChanged, autostartChanged, sessionsChanged bool) error {
			failures := make([]string, 0, 4)
			if sessionsChanged && s.RemoteAccess != nil {
				if restoreErr := s.restoreSessions(remoteSnapshot); restoreErr != nil {
					failures = append(failures, "恢复局域网会话失败: "+restoreErr.Error())
				}
			}
			if autostartChanged {
				if restoreErr := s.syncAutostart(current.Autostart, current.SilentAutostart); restoreErr != nil {
					failures = append(failures, "恢复自动启动失败: "+restoreErr.Error())
				}
			}
			if listenerChanged {
				if restoreErr := s.reconfigureLANAccessForSettings(current.LANAccessEnabled); restoreErr != nil {
					failures = append(failures, "恢复监听失败: "+restoreErr.Error())
				}
			}
			if len(failures) > 0 {
				return fmt.Errorf("%v；回滚失败: %s", primary, strings.Join(failures, "；"))
			}
			return primary
		}

		listenerChanged := current.LANAccessEnabled != next.LANAccessEnabled
		if listenerChanged {
			if err := s.reconfigureLANAccessForSettings(next.LANAccessEnabled); err != nil {
				writeError(w, fmt.Errorf("切换局域网监听失败: %w", err), http.StatusInternalServerError)
				return
			}
		}
		autostartChanged := current.Autostart != next.Autostart || current.SilentAutostart != next.SilentAutostart
		if autostartChanged {
			if err := s.syncAutostart(next.Autostart, next.SilentAutostart); err != nil {
				writeError(w, rollback(err, listenerChanged, false, false), http.StatusInternalServerError)
				return
			}
		}
		sessionsChanged := current.LANAccessEnabled && !next.LANAccessEnabled && s.RemoteAccess != nil
		if sessionsChanged {
			if err := s.resetSessions(); err != nil {
				writeError(w, rollback(fmt.Errorf("撤销局域网会话失败: %w", err), listenerChanged, autostartChanged, false), http.StatusInternalServerError)
				return
			}
		}
		updated, err := s.persistSettings(next)
		if err != nil {
			writeError(w, rollback(err, listenerChanged, autostartChanged, sessionsChanged), http.StatusInternalServerError)
			return
		}
		s.changed()
		writeJSON(w, updated)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) reconfigureLANAccessForSettings(enabled bool) error {
	if s.reconfigureLAN != nil {
		return s.reconfigureLAN(enabled)
	}
	return s.reconfigureLANAccess(enabled)
}

func (s *Server) syncAutostart(enabled, silent bool) error {
	if s.autostartSync != nil {
		return s.autostartSync(enabled, s.ExePath, silent)
	}
	return autostart.Sync(enabled, s.ExePath, silent)
}

func (s *Server) resetSessions() error {
	if s.resetRemoteSessions != nil {
		return s.resetRemoteSessions()
	}
	return s.RemoteAccess.ResetSessions()
}

func (s *Server) restoreSessions(snapshot remoteaccess.Snapshot) error {
	if s.restoreRemoteSessions != nil {
		return s.restoreRemoteSessions(snapshot)
	}
	return s.RemoteAccess.Restore(snapshot)
}

func (s *Server) persistSettings(next settings.Settings) (settings.Settings, error) {
	if s.updateSettings != nil {
		return s.updateSettings(next)
	}
	return s.Settings.Update(next)
}

func (s *Server) handleFetchModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ProfileID      string `json:"profile_id"`
		BaseURL        string `json:"base_url"`
		APIKey         string `json:"api_key"`
		UpstreamFormat string `json:"upstream_format"`
	}
	if err := decodeManagementJSON(w, r, &req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if req.ProfileID != "" {
		profile, err := s.Profiles.Get(req.ProfileID)
		if err != nil {
			status := http.StatusInternalServerError
			if os.IsNotExist(err) {
				status = http.StatusNotFound
			}
			writeError(w, err, status)
			return
		}
		if req.BaseURL == "" {
			req.BaseURL = profile.BaseURL
		}
		if req.APIKey == "" {
			req.APIKey = profile.EffectiveAPIKey()
		}
		if req.UpstreamFormat == "" {
			req.UpstreamFormat = profile.UpstreamFormat
		}
	}
	models, err := fetchModelList(r.Context(), req.BaseURL, req.APIKey, req.UpstreamFormat)
	if err != nil {
		writeError(w, err, http.StatusBadGateway)
		return
	}
	if req.ProfileID != "" {
		profile, err := s.Profiles.Get(req.ProfileID)
		if err != nil {
			writeError(w, fmt.Errorf("模型已获取，但读取 Profile 失败: %w", err), http.StatusInternalServerError)
			return
		}
		profile.AvailableModels = models
		if _, err := s.Profiles.Update(req.ProfileID, profile); err != nil {
			writeError(w, fmt.Errorf("模型已获取，但保存到 Profile 失败: %w", err), http.StatusInternalServerError)
			return
		}
		s.changed()
	}
	writeJSON(w, map[string]any{"models": models})
}

type reasoningEffortProbeResult struct {
	Effort string `json:"effort"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type reasoningEffortsResponse struct {
	Efforts []string                     `json:"efforts"`
	Source  string                       `json:"source"`
	Note    string                       `json:"note"`
	Results []reasoningEffortProbeResult `json:"results,omitempty"`
}

var reasoningEffortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

func (s *Server) handleReasoningEfforts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ProfileID          string `json:"profile_id"`
		BaseURL            string `json:"base_url"`
		APIKey             string `json:"api_key"`
		UpstreamFormat     string `json:"upstream_format"`
		Model              string `json:"model"`
		APIBackend         string `json:"api_backend"`
		UserConfirmedProbe bool   `json:"user_confirmed_probe"`
	}
	if err := decodeManagementJSON(w, r, &req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	var profile profiles.Profile
	if req.ProfileID != "" && s.Profiles != nil {
		if stored, err := s.Profiles.Get(req.ProfileID); err == nil {
			profile = stored
			if req.BaseURL == "" {
				req.BaseURL = stored.BaseURL
			}
			if req.APIKey == "" {
				req.APIKey = stored.EffectiveAPIKey()
			}
			if req.UpstreamFormat == "" {
				req.UpstreamFormat = stored.UpstreamFormat
			}
		}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		writeError(w, fmt.Errorf("model is required"), http.StatusBadRequest)
		return
	}
	for _, def := range profile.Models {
		if def.Model != model && def.Name != model {
			continue
		}
		if req.BaseURL == "" {
			req.BaseURL = def.BaseURL
		}
		if req.APIKey == "" {
			req.APIKey = def.APIKey
		}
		if req.APIBackend == "" {
			req.APIBackend = def.APIBackend
		}
		if def.SupportsReasoningEffort && def.ReasoningEffortsSource == "declared" {
			if efforts := normalizeReasoningEfforts(def.ReasoningEfforts); len(efforts) > 0 {
				writeJSON(w, reasoningEffortsResponse{Efforts: efforts, Source: "declared", Note: "使用 Profile 中该模型声明的推理强度，未向上游发送探测请求。"})
				return
			}
		}
		break
	}
	backend := req.APIBackend
	if backend == "" {
		backend = profiles.APIBackendForUpstreamFormat(req.UpstreamFormat)
	}
	if backend == "messages" {
		writeJSON(w, reasoningEffortsResponse{Efforts: []string{}, Source: "unknown", Note: "messages 后端没有可安全通用探测的 reasoning_effort 字段，未发送探测请求。"})
		return
	}
	if strings.TrimSpace(req.BaseURL) == "" {
		writeError(w, fmt.Errorf("base_url is required"), http.StatusBadRequest)
		return
	}
	if err := rejectOfficialAnthropic(req.BaseURL); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if !req.UserConfirmedProbe {
		writeError(w, fmt.Errorf("需要明确确认后才能发送推理强度探测请求"), http.StatusBadRequest)
		return
	}
	client := &http.Client{Timeout: 12 * time.Second}
	results := make([]reasoningEffortProbeResult, 0, len(reasoningEffortOrder))
	accepted := make([]string, 0, len(reasoningEffortOrder))
	for _, effort := range reasoningEffortOrder {
		result := probeReasoningEffort(r.Context(), client, req.BaseURL, req.APIKey, backend, model, effort)
		results = append(results, result)
		if result.Status == "accepted" {
			accepted = append(accepted, effort)
		}
	}
	source := "probe"
	if len(accepted) == 0 {
		source = "unknown"
	}
	writeJSON(w, reasoningEffortsResponse{Efforts: accepted, Source: source, Note: "accepted 仅表示上游接受了请求；上游仍可能静默忽略 reasoning_effort。", Results: results})
}

func normalizeReasoningEfforts(values []string) []string {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		seen[strings.ToLower(strings.TrimSpace(value))] = true
	}
	out := make([]string, 0, len(reasoningEffortOrder))
	for _, effort := range reasoningEffortOrder {
		if seen[effort] {
			out = append(out, effort)
		}
	}
	return out
}

func probeReasoningEffort(ctx context.Context, client *http.Client, baseURL, apiKey, backend, model, effort string) reasoningEffortProbeResult {
	result := reasoningEffortProbeResult{Effort: effort, Status: "unknown"}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	body := map[string]any{"model": model, "reasoning_effort": effort}
	endpoint := baseURL + "/chat/completions"
	if backend == "responses" {
		endpoint = baseURL + "/responses"
		body["input"] = "ping"
		body["max_output_tokens"] = 1
	} else {
		body["messages"] = []map[string]string{{"role": "user", "content": "ping"}}
		body["max_tokens"] = 1
	}
	payload, err := json.Marshal(body)
	if err != nil {
		result.Error = "无法编码探测请求"
		return result
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		result.Error = "无法创建探测请求"
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("x-api-key", apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		result.Error = "上游网络请求失败或超时"
		return result
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		result.Status = "accepted"
		return result
	}
	message := strings.TrimSpace(string(raw))
	if apiKey != "" {
		message = strings.ReplaceAll(message, apiKey, "[REDACTED]")
	}
	lower := strings.ToLower(message)
	if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
		(strings.Contains(lower, "unsupported") || strings.Contains(lower, "invalid") || strings.Contains(lower, "unknown")) &&
		(strings.Contains(lower, "reasoning_effort") || strings.Contains(lower, "reasoning effort") || strings.Contains(lower, "effort")) {
		result.Status = "unsupported"
	}
	if message == "" {
		result.Error = resp.Status
	} else {
		result.Error = fmt.Sprintf("%s: %s", resp.Status, message)
	}
	return result
}

func (s *Server) handleConnectionTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ProfileID      string `json:"profile_id"`
		BaseURL        string `json:"base_url"`
		APIKey         string `json:"api_key"`
		UpstreamFormat string `json:"upstream_format"`
		Model          string `json:"model"`
		APIBackend     string `json:"api_backend"`
	}
	if err := decodeManagementJSON(w, r, &req); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if req.ProfileID != "" {
		if profile, err := s.Profiles.Get(req.ProfileID); err == nil {
			if req.BaseURL == "" {
				req.BaseURL = profile.BaseURL
			}
			if req.APIKey == "" {
				req.APIKey = profile.EffectiveAPIKey()
			}
			if req.UpstreamFormat == "" {
				req.UpstreamFormat = profile.UpstreamFormat
			}
		}
	}
	start := time.Now()
	// Per-model probe: send a minimal completion request.
	if strings.TrimSpace(req.Model) != "" {
		err := probeModel(r.Context(), req.BaseURL, req.APIKey, req.UpstreamFormat, req.APIBackend, req.Model)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			writeJSONStatus(w, map[string]any{
				"ok":         false,
				"latency_ms": latency,
				"error":      err.Error(),
				"model":      req.Model,
			}, http.StatusOK)
			return
		}
		writeJSON(w, map[string]any{
			"ok":         true,
			"latency_ms": latency,
			"model":      req.Model,
		})
		return
	}
	models, err := fetchModelList(r.Context(), req.BaseURL, req.APIKey, req.UpstreamFormat)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		writeJSONStatus(w, map[string]any{
			"ok":            false,
			"latency_ms":    latency,
			"error":         err.Error(),
			"model_count":   0,
			"sample_models": []string{},
		}, http.StatusOK)
		return
	}
	sample := models
	if len(sample) > 5 {
		sample = sample[:5]
	}
	writeJSON(w, map[string]any{
		"ok":            true,
		"latency_ms":    latency,
		"model_count":   len(models),
		"sample_models": sample,
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		data, err := s.Switcher.ReadConfig()
		if err != nil && !os.IsNotExist(err) {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		exists := err == nil
		if os.IsNotExist(err) {
			data = []byte{}
		}
		writeJSON(w, map[string]any{
			"path":    s.Paths.GrokConfig,
			"content": string(data),
			"exists":  exists,
		})
	case http.MethodPut:
		var req struct {
			Content string `json:"content"`
		}
		if err := decodeConfigJSON(w, r, &req); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Content) != "" {
			var probe map[string]any
			if err := toml.Unmarshal([]byte(req.Content), &probe); err != nil {
				writeError(w, fmt.Errorf("TOML 无效: %w", err), http.StatusBadRequest)
				return
			}
		}
		if err := grokconfig.ValidateProfileEndpointsText([]byte(req.Content)); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		s.collaborationMu.Lock()
		s.routingMu.Lock()
		err := s.Switcher.WriteConfig([]byte(req.Content))
		s.routingMu.Unlock()
		s.collaborationMu.Unlock()
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		s.changed()
		writeJSON(w, map[string]any{"ok": true, "path": s.Paths.GrokConfig})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleConfigPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var profile profiles.Profile
	if err := decodeManagementJSON(w, r, &profile); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	profile = profiles.Normalize(profile)
	if err := profiles.ValidateEndpoints(profile); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	snippet, err := grokconfig.SnippetForProfile(profile)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	full, err := grokconfig.PreviewApply(s.Paths.GrokConfig, profile)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"path":    s.Paths.GrokConfig,
		"snippet": snippet,
		"full":    string(full),
		"note":    "磁盘上只有一份生效的 config.toml。每个供应商的 URL、Key 和模型保存在 grok_switch 的 profile 里；保存供应商不会切换当前路由，实际使用的模型由“模型路由”统一管理。",
	})
}

func (s *Server) handleConfigPrivacy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	s.collaborationMu.Lock()
	s.routingMu.Lock()
	err := s.Switcher.ApplyPrivacyProtection()
	s.routingMu.Unlock()
	s.collaborationMu.Unlock()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	s.changed()
	writeJSON(w, map[string]any{
		"ok":      true,
		"path":    s.Paths.GrokConfig,
		"message": "隐私保护配置已写入 config.toml",
	})
}

func fetchModelList(ctx context.Context, baseURL, apiKey, upstreamFormat string) ([]string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if err := rejectOfficialAnthropic(baseURL); err != nil {
		return nil, err
	}
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is required")
	}
	client := &http.Client{Timeout: 12 * time.Second}
	var failures []string
	for _, endpoint := range modelEndpoints(baseURL) {
		models, err := fetchModelEndpoint(ctx, client, endpoint, apiKey)
		if err == nil && len(models) > 0 {
			return models, nil
		}
		if err != nil {
			failures = append(failures, endpoint+": "+err.Error())
		} else {
			failures = append(failures, endpoint+": empty model list")
		}
	}
	return nil, fmt.Errorf("failed to fetch %s model list: %s", upstreamFormat, strings.Join(failures, "; "))
}

func probeModel(ctx context.Context, baseURL, apiKey, upstreamFormat, apiBackend, model string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if err := rejectOfficialAnthropic(baseURL); err != nil {
		return err
	}
	model = strings.TrimSpace(model)
	if baseURL == "" {
		return fmt.Errorf("base_url is required")
	}
	if model == "" {
		return fmt.Errorf("model is required")
	}
	backend := apiBackend
	if backend == "" {
		backend = profiles.APIBackendForUpstreamFormat(upstreamFormat)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	var endpoint string
	var body map[string]any
	headers := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	switch backend {
	case "messages":
		endpoint = baseURL + "/messages"
		// Some gateways expect /v1/messages already in base; also try raw.
		if !strings.HasSuffix(baseURL, "/v1") && !strings.Contains(baseURL, "/messages") {
			// keep as-is; many anthropic-compat proxies use /v1 base already
		}
		if strings.HasSuffix(baseURL, "/v1") {
			endpoint = baseURL + "/messages"
		}
		body = map[string]any{
			"model":      model,
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		}
		if apiKey != "" {
			headers["x-api-key"] = apiKey
			headers["Authorization"] = "Bearer " + apiKey
			headers["anthropic-version"] = "2023-06-01"
		}
	case "responses":
		endpoint = baseURL + "/responses"
		body = map[string]any{
			"model":             model,
			"input":             "ping",
			"max_output_tokens": 1,
		}
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}
	default:
		endpoint = baseURL + "/chat/completions"
		body = map[string]any{
			"model":      model,
			"max_tokens": 1,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		}
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
			headers["x-api-key"] = apiKey
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(raw))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("%s: %s", resp.Status, msg)
}

func rejectOfficialAnthropic(baseURL string) error {
	return profiles.ValidateEndpoints(profiles.Profile{BaseURL: baseURL})
}

func modelEndpoints(baseURL string) []string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	candidates := []string{baseURL + "/models"}
	if !strings.HasSuffix(baseURL, "/v1") {
		candidates = append(candidates, baseURL+"/v1/models")
	}
	return uniqueStrings(candidates)
}

func fetchModelEndpoint(ctx context.Context, client *http.Client, endpoint, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("x-api-key", apiKey)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("returned %s", resp.Status)
	}
	var payload any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	models := extractModels(payload)
	if len(models) == 0 {
		return nil, fmt.Errorf("no models in response")
	}
	return models, nil
}

func extractModels(payload any) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			id, _ := x["id"].(string)
			id = strings.TrimSpace(id)
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
			for key, child := range x {
				if key == "data" || key == "models" {
					walk(child)
				}
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		case string:
			if x != "" && !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	walk(payload)
	return out
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" {
		name = "ui/index.html"
	} else if name == "icon.svg" {
		name = "icon.svg"
	} else {
		name = "ui/" + name
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// Always revalidate UI assets so drift fixes and UI changes apply without hard cache.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	data, err := fs.ReadFile(s.Assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Write(data)
}

func (s *Server) changed() {
	if s.onChanged != nil {
		s.onChanged()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, v, http.StatusOK)
}

func writeJSONStatus(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error, status int) {
	writeJSONStatus(w, map[string]string{"error": err.Error()}, status)
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, fmt.Errorf("method not allowed"), http.StatusMethodNotAllowed)
}
