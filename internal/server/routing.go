package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	grokconfig "grok_switch/internal/config"
	"grok_switch/internal/grokauth"
	"grok_switch/internal/httpjson"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
)

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

type routingProviderDTO struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ProfileID      string `json:"profile_id"`
	Source         string `json:"source,omitempty"`
	UpstreamFormat string `json:"upstream_format,omitempty"`
}

type routingModelDTO struct {
	ID                      string   `json:"id"`
	Name                    string   `json:"name"`
	ProviderID              string   `json:"provider_id"`
	ProfileModel            string   `json:"profile_model"`
	Model                   string   `json:"model"`
	APIBackend              string   `json:"api_backend"`
	SupportsBackendSearch   bool     `json:"supports_backend_search"`
	SupportsReasoningEffort bool     `json:"supports_reasoning_effort"`
	ReasoningEfforts        []string `json:"reasoning_efforts,omitempty"`
	ReasoningEffortsSource  string   `json:"reasoning_efforts_source,omitempty"`
	ContextWindow           int64    `json:"context_window,omitempty"`
	MaxCompletionTokens     int64    `json:"max_completion_tokens,omitempty"`
}

type routingSnapshotDTO struct {
	Version          int                              `json:"version"`
	ActiveProviderID string                           `json:"active_provider_id"`
	Providers        []routingProviderDTO             `json:"providers"`
	ModelRoutes      []routingModelDTO                `json:"model_routes"`
	OfficialModels   []routingModelDTO                `json:"official_models,omitempty"`
	OfficialLoggedIn bool                             `json:"official_logged_in"`
	Policy           routing.RoutingPolicy            `json:"policy"`
	ProviderPolicies map[string]routing.RoutingPolicy `json:"provider_policies"`
	RepairRequired   bool                             `json:"repair_required"`
	SuggestedPolicy  *routing.RoutingPolicy           `json:"suggested_policy,omitempty"`
	WebSearchCapable bool                             `json:"web_search_capable"`
	UpdatedAt        time.Time                        `json:"updated_at"`
}

func (s *Server) handleRouting(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	dto, _, err := s.currentRouting()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, dto)
}

func (s *Server) handleRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	if !isLoopbackRequest(r) {
		writeError(w, fmt.Errorf("仅允许本机修改路由策略"), http.StatusForbidden)
		return
	}
	if s.Routing == nil || s.Profiles == nil || s.Switcher == nil {
		writeError(w, fmt.Errorf("路由服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	s.routingMu.Lock()
	defer s.routingMu.Unlock()

	// Support partial updates while strictly validating field names and types.
	var rawPatch struct {
		ActiveProviderID       *string `json:"active_provider_id"`
		Official               *bool   `json:"official"`
		Default                *string `json:"default"`
		DefaultReasoningEffort *string `json:"default_reasoning_effort"`
		WebSearch              *string `json:"web_search"`
		WebSearchCapable       *bool   `json:"web_search_capable"`
		Subagents              *struct {
			Explore *string `json:"explore"`
			Plan    *string `json:"plan"`
		} `json:"subagents"`
	}
	if err := decodeRoutingJSON(w, r, &rawPatch); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	profilesList, err := s.Profiles.List()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	currentStored, err := s.Routing.Snapshot()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	activeProviderID := currentStored.ActiveProviderID
	if rawPatch.ActiveProviderID != nil {
		activeProviderID = strings.TrimSpace(*rawPatch.ActiveProviderID)
	}
	if rawPatch.Official != nil {
		if *rawPatch.Official {
			activeProviderID = routing.OfficialProviderID
		} else if activeProviderID == routing.OfficialProviderID {
			activeProviderID = ""
		}
	}
	if activeProviderID == "" && rawPatch.Default != nil {
		if route, ok := currentStored.Route(*rawPatch.Default); ok {
			activeProviderID = route.ProviderID
		}
	}
	if activeProviderID == "" {
		writeError(w, fmt.Errorf("请选择要启用的供应商"), http.StatusBadRequest)
		return
	}
	if activeProviderID != routing.OfficialProviderID {
		if _, ok := currentStored.Provider(activeProviderID); !ok {
			writeError(w, fmt.Errorf("供应商不可用"), http.StatusBadRequest)
			return
		}
	}
	// Merge only the selected provider's remembered policy.
	policy := currentStored.ProviderPolicies[activeProviderID]
	policy.Official = activeProviderID == routing.OfficialProviderID
	resolveRef := func(ref string) (string, error) {
		ref = strings.TrimSpace(ref)
		if ref == "" || activeProviderID == routing.OfficialProviderID {
			return ref, nil
		}
		route, ok := currentStored.Route(ref)
		if !ok || route.ProviderID != activeProviderID {
			return "", fmt.Errorf("模型 %q 不属于当前供应商", ref)
		}
		return route.ID, nil
	}
	if rawPatch.Default != nil {
		ref, resolveErr := resolveRef(*rawPatch.Default)
		if resolveErr != nil {
			writeError(w, resolveErr, http.StatusBadRequest)
			return
		}
		policy.Default = ref
	}
	if rawPatch.DefaultReasoningEffort != nil {
		policy.DefaultReasoningEffort = *rawPatch.DefaultReasoningEffort
	}
	if rawPatch.WebSearch != nil {
		ref, resolveErr := resolveRef(*rawPatch.WebSearch)
		if resolveErr != nil {
			writeError(w, resolveErr, http.StatusBadRequest)
			return
		}
		policy.WebSearch = ref
	}
	if rawPatch.Subagents != nil {
		if rawPatch.Subagents.Explore != nil {
			ref, resolveErr := resolveRef(*rawPatch.Subagents.Explore)
			if resolveErr != nil {
				writeError(w, resolveErr, http.StatusBadRequest)
				return
			}
			policy.Subagents.Explore = ref
		}
		if rawPatch.Subagents.Plan != nil {
			ref, resolveErr := resolveRef(*rawPatch.Subagents.Plan)
			if resolveErr != nil {
				writeError(w, resolveErr, http.StatusBadRequest)
				return
			}
			policy.Subagents.Plan = ref
		}
	}
	if activeProviderID == routing.OfficialProviderID {
		if _, loggedIn := s.officialRoutingModels(); !loggedIn {
			writeError(w, fmt.Errorf("尚未登录 Grok 官方账号"), http.StatusBadRequest)
			return
		}
		if err := validateOfficialRoutingPolicy(policy); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
	}

	nextState := currentStored
	nextState.Policy = routing.RoutingPolicy{}
	nextState.ActiveProviderID = activeProviderID
	if nextState.ProviderPolicies == nil {
		nextState.ProviderPolicies = map[string]routing.RoutingPolicy{}
	}
	policy.Official = false
	nextState.ProviderPolicies[activeProviderID] = policy
	hydrated, err := routing.ProjectWithSnapshot(profilesList, nextState)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if err := validateRoutingReasoningEffort(hydrated); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	// Generate the complete next document before touching either durable state.
	if _, err := grokconfig.PreviewRouting(s.Switcher.ConfigPath, hydrated); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	oldConfig, readErr := os.ReadFile(s.Switcher.ConfigPath)
	configExisted := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		writeError(w, readErr, http.StatusInternalServerError)
		return
	}
	if err := s.Switcher.ApplyRouting(hydrated); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	stored, err := s.Routing.Replace(hydrated)
	if err != nil {
		if rollbackErr := s.Switcher.RestoreConfigState(oldConfig, configExisted); rollbackErr != nil {
			err = fmt.Errorf("保存路由策略失败: %v；恢复原配置失败: %w", err, rollbackErr)
		}
		writeError(w, err, http.StatusInternalServerError)
		return
	}

	hydrated, err = routing.ProjectWithSnapshot(profilesList, stored)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	hydrated.Version = stored.Version
	hydrated.UpdatedAt = stored.UpdatedAt
	s.changed()
	writeJSON(w, s.routingDTO(hydrated))
}

func (s *Server) handleRoutingReapply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !isLoopbackRequest(r) {
		writeError(w, fmt.Errorf("仅允许本机操作"), http.StatusForbidden)
		return
	}
	if s.Routing == nil || s.Profiles == nil || s.Switcher == nil {
		writeError(w, fmt.Errorf("路由服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	if err := s.applyCurrentRoutingLocked(); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	s.changed()
	writeJSON(w, map[string]any{"message": "路由策略已重新应用"})
}

// ApplyCurrentRouting rebuilds the catalog after profile migrations. Policy
// fields that still resolve are retained; invalid optional fields are cleared,
// while default falls back to the active profile and then the first route.
func (s *Server) ApplyCurrentRouting() error {
	if s.Routing == nil || s.Profiles == nil || s.Switcher == nil {
		return nil
	}
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	return s.applyCurrentRoutingLocked()
}

func (s *Server) applyCurrentRoutingLocked() error {
	stored, err := s.Routing.Snapshot()
	if err != nil {
		return err
	}
	profileList, err := s.Profiles.List()
	if err != nil {
		return err
	}
	_, err = s.applyRoutingSnapshotTransaction(profileList, stored)
	return err
}

func (s *Server) applyRoutingPolicyTransaction(profileList []profiles.Profile, policy routing.RoutingPolicy) (routing.Snapshot, error) {
	current, err := s.Routing.Snapshot()
	if err != nil {
		return routing.Snapshot{}, err
	}
	providerID := current.ActiveProviderID
	if policy.Official {
		providerID = routing.OfficialProviderID
		policy.Official = false
	} else {
		for _, ref := range []string{policy.Default, policy.WebSearch, policy.Subagents.Explore, policy.Subagents.Plan} {
			if route, ok := current.Route(ref); ok {
				providerID = route.ProviderID
				break
			}
		}
	}
	current.Policy = routing.RoutingPolicy{}
	current.ActiveProviderID = providerID
	if current.ProviderPolicies == nil {
		current.ProviderPolicies = map[string]routing.RoutingPolicy{}
	}
	current.ProviderPolicies[providerID] = policy
	return s.applyRoutingSnapshotTransaction(profileList, current)
}

func (s *Server) applyRoutingSnapshotTransaction(profileList []profiles.Profile, state routing.Snapshot) (routing.Snapshot, error) {
	if state.IsOfficial() {
		if err := validateOfficialRoutingPolicy(state.ActivePolicy()); err != nil {
			return routing.Snapshot{}, err
		}
	}
	hydrated, err := routing.ProjectWithSnapshot(profileList, state)
	if err != nil {
		return routing.Snapshot{}, err
	}
	if err := validateRoutingReasoningEffort(hydrated); err != nil {
		return routing.Snapshot{}, err
	}
	if _, err := grokconfig.PreviewRouting(s.Switcher.ConfigPath, hydrated); err != nil {
		return routing.Snapshot{}, err
	}
	oldConfig, readErr := os.ReadFile(s.Switcher.ConfigPath)
	configExisted := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return routing.Snapshot{}, readErr
	}
	if err := s.Switcher.ApplyRouting(hydrated); err != nil {
		return routing.Snapshot{}, err
	}
	stored, err := s.Routing.Replace(hydrated)
	if err != nil {
		if rollbackErr := s.Switcher.RestoreConfigState(oldConfig, configExisted); rollbackErr != nil {
			return routing.Snapshot{}, fmt.Errorf("保存路由策略失败: %v；恢复原配置失败: %w", err, rollbackErr)
		}
		return routing.Snapshot{}, err
	}
	hydrated.Version = stored.Version
	hydrated.UpdatedAt = stored.UpdatedAt
	return hydrated, nil
}

func hydratedRouteProviderID(snapshot routing.Snapshot) string {
	route, ok := snapshot.Route(snapshot.ActivePolicy().Default)
	if !ok {
		return ""
	}
	return route.ProviderID
}

func routeForProfile(snapshot routing.Snapshot, profileID, model string) (routing.ModelRoute, bool) {
	for _, provider := range snapshot.Providers {
		if provider.ProfileID != profileID {
			continue
		}
		for _, route := range snapshot.ModelRoutes {
			if route.ProviderID == provider.ID && route.ProfileModel == model {
				return route, true
			}
		}
	}
	return routing.ModelRoute{}, false
}

func (s *Server) currentRouting() (routingSnapshotDTO, routing.Snapshot, error) {
	if s.Routing == nil || s.Profiles == nil {
		return routingSnapshotDTO{}, routing.Snapshot{}, fmt.Errorf("路由服务未初始化")
	}
	s.routingMu.Lock()
	defer s.routingMu.Unlock()

	stored, err := s.Routing.Snapshot()
	if err != nil {
		return routingSnapshotDTO{}, routing.Snapshot{}, err
	}
	profilesList, err := s.Profiles.List()
	if err != nil {
		return routingSnapshotDTO{}, routing.Snapshot{}, err
	}
	hydrated, err := routing.ProjectWithSnapshot(profilesList, stored)
	if err != nil {
		return routingSnapshotDTO{}, routing.Snapshot{}, err
	}
	hydrated.Version = stored.Version
	hydrated.UpdatedAt = stored.UpdatedAt
	dto := s.routingDTO(hydrated)
	if hydrated.ActiveProviderID != stored.ActiveProviderID || hydrated.ActivePolicy() != stored.ActivePolicy() {
		dto.RepairRequired = true
		suggested := hydrated.ActivePolicy()
		dto.SuggestedPolicy = &suggested
	}
	return dto, hydrated, nil
}

func (s *Server) routingDTO(snapshot routing.Snapshot) routingSnapshotDTO {
	providers := make([]routingProviderDTO, 0, len(snapshot.Providers))
	for _, provider := range snapshot.Providers {
		providers = append(providers, routingProviderDTO{
			ID: provider.ID, Name: provider.Name, ProfileID: provider.ProfileID,
			Source: provider.Source, UpstreamFormat: provider.UpstreamFormat,
		})
	}
	models := make([]routingModelDTO, 0, len(snapshot.ModelRoutes))
	for _, route := range snapshot.ModelRoutes {
		models = append(models, routingModelDTO{
			ID: route.ID, Name: route.Name, ProviderID: route.ProviderID,
			ProfileModel: route.ProfileModel, Model: route.Model, APIBackend: route.APIBackend,
			SupportsBackendSearch:   route.SupportsBackendSearch,
			SupportsReasoningEffort: route.SupportsReasoningEffort,
			ReasoningEfforts:        append([]string(nil), route.ReasoningEfforts...),
			ReasoningEffortsSource:  route.ReasoningEffortsSource,
			ContextWindow:           route.ContextWindow, MaxCompletionTokens: route.MaxCompletionTokens,
		})
	}
	officialModels, officialLoggedIn := s.officialRoutingModels()
	policy := snapshot.ActivePolicy()
	providerPolicies := make(map[string]routing.RoutingPolicy, len(snapshot.ProviderPolicies))
	for providerID, remembered := range snapshot.ProviderPolicies {
		providerPolicies[providerID] = remembered
	}
	return routingSnapshotDTO{
		Version: snapshot.Version, ActiveProviderID: snapshot.ActiveProviderID, Providers: providers, ModelRoutes: models,
		OfficialModels: officialModels, OfficialLoggedIn: officialLoggedIn,
		Policy: policy, ProviderPolicies: providerPolicies, WebSearchCapable: policy.WebSearchCapable,
		UpdatedAt: snapshot.UpdatedAt,
	}
}

func validateRoutingReasoningEffort(snapshot routing.Snapshot) error {
	policy := snapshot.ActivePolicy()
	effort := strings.TrimSpace(policy.DefaultReasoningEffort)
	if effort == "" || effort == "none" {
		return nil
	}
	if snapshot.IsOfficial() {
		for _, model := range defaultOfficialRoutingModels {
			if model.Name == policy.Default {
				if containsReasoningEffort(model.ReasoningEfforts, effort) {
					return nil
				}
				return fmt.Errorf("官方模型 %q 不支持推理强度 %q；可用档位：%s", model.Name, effort, strings.Join(model.ReasoningEfforts, "、"))
			}
		}
		return fmt.Errorf("官方默认模型 %q 不可用", policy.Default)
	}
	route, ok := snapshot.Route(policy.Default)
	if !ok {
		return fmt.Errorf("默认路由模型 %q 不可用", policy.Default)
	}
	if containsReasoningEffort(route.ReasoningEfforts, effort) {
		return nil
	}
	return fmt.Errorf("模型 %q 不支持推理强度 %q；可用档位：%s", route.Name, effort, strings.Join(route.ReasoningEfforts, "、"))
}

func containsReasoningEffort(efforts []string, target string) bool {
	for _, effort := range efforts {
		if effort == target {
			return true
		}
	}
	return false
}

func validateOfficialRoutingPolicy(policy routing.RoutingPolicy) error {
	allowed := make(map[string]bool, len(defaultOfficialRoutingModels))
	for _, model := range defaultOfficialRoutingModels {
		allowed[model.Name] = true
	}
	for label, model := range map[string]string{
		"default":           policy.Default,
		"web_search":        policy.WebSearch,
		"subagents.explore": policy.Subagents.Explore,
		"subagents.plan":    policy.Subagents.Plan,
	} {
		if model != "" && !allowed[model] {
			return fmt.Errorf("官方路由 %s 引用了不可用模型 %q", label, model)
		}
	}
	if policy.Default == "" {
		return fmt.Errorf("请选择官方默认模型")
	}
	return nil
}

func (s *Server) resolveRoutingModel(snapshot routing.Snapshot, name string) (routing.ModelRoute, bool) {
	if !snapshot.IsOfficial() {
		return snapshot.Route(name)
	}
	baseURL, apiKey := "", ""
	extraHeaders := map[string]string(nil)
	if credential, ok := s.nativeOfficialCredential(); ok {
		baseURL = strings.TrimRight(grokauth.UpstreamURL(), "/")
		apiKey = credential.AccessToken
		extraHeaders = map[string]string{
			"X-XAI-Token-Auth":      "xai-grok-cli",
			"x-grok-client-version": "0.2.93",
			"User-Agent":            "xai-grok-workspace/0.2.93",
		}
	}
	if baseURL == "" || apiKey == "" {
		return routing.ModelRoute{}, false
	}
	for _, model := range defaultOfficialRoutingModels {
		if model.Name == name || model.Model == name {
			return routing.ModelRoute{
				ID: model.Name, Name: model.Name, ProviderID: "official", ProfileModel: model.Name,
				Model: model.Model, APIBackend: "responses", BaseURL: baseURL, APIKey: apiKey, ExtraHeaders: extraHeaders,
				SupportsBackendSearch: model.SupportsBackendSearch, SupportsReasoningEffort: model.SupportsReasoningEffort,
				ReasoningEfforts: append([]string(nil), model.ReasoningEfforts...), ReasoningEffortsSource: model.ReasoningEffortsSource,
				ContextWindow: model.ContextWindow, MaxCompletionTokens: model.MaxCompletionTokens,
			}, true
		}
	}
	return routing.ModelRoute{}, false
}

func (s *Server) nativeOfficialCredential() (grokauth.Credential, bool) {
	if s.Paths.GrokHome == "" {
		return grokauth.Credential{}, false
	}
	raw, err := os.ReadFile(filepath.Join(s.Paths.GrokHome, "auth.json"))
	if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
		return grokauth.Credential{}, false
	}
	credential, err := grokauth.ParseCredential(raw)
	if err != nil || strings.TrimSpace(credential.AccessToken) == "" {
		return grokauth.Credential{}, false
	}
	if !credential.ExpiresAt.IsZero() && !credential.ExpiresAt.After(time.Now()) {
		return grokauth.Credential{}, false
	}
	return credential, true
}

func (s *Server) officialLoggedIn() bool {
	_, ok := s.nativeOfficialCredential()
	return ok
}

func (s *Server) officialRoutingModels() ([]routingModelDTO, bool) {
	if !s.officialLoggedIn() {
		return nil, false
	}
	models := make([]routingModelDTO, 0, len(defaultOfficialRoutingModels))
	for _, model := range defaultOfficialRoutingModels {
		models = append(models, routingModelDTO{
			ID: model.Name, Name: model.Name, ProviderID: "official", ProfileModel: model.Name,
			Model: model.Model, APIBackend: "responses",
			SupportsBackendSearch:   model.SupportsBackendSearch,
			SupportsReasoningEffort: model.SupportsReasoningEffort,
			ReasoningEfforts:        append([]string(nil), model.ReasoningEfforts...),
			ReasoningEffortsSource:  model.ReasoningEffortsSource,
			ContextWindow:           model.ContextWindow, MaxCompletionTokens: model.MaxCompletionTokens,
		})
	}
	return models, true
}

func decodeRoutingJSON(w http.ResponseWriter, r *http.Request, out any) error {
	if err := httpjson.Decode(w, r, out, httpjson.Options{MaxBytes: 32 << 10}); err != nil {
		return fmt.Errorf("请求格式无效: %w", err)
	}
	return nil
}
