package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	grokconfig "grok_switch/internal/config"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
)

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

type browserUseStatusDTO struct {
	Available bool   `json:"available"`
	Command   string `json:"command,omitempty"`
	Error     string `json:"error,omitempty"`
}

type routingSnapshotDTO struct {
	Version          int                   `json:"version"`
	Providers        []routingProviderDTO  `json:"providers"`
	ModelRoutes      []routingModelDTO     `json:"model_routes"`
	OfficialModels   []routingModelDTO     `json:"official_models,omitempty"`
	OfficialLoggedIn bool                  `json:"official_logged_in"`
	Policy           routing.RoutingPolicy `json:"policy"`
	WebSearchCapable bool                  `json:"web_search_capable"`
	BrowserUse       browserUseStatusDTO   `json:"browser_use"`
	UpdatedAt        time.Time             `json:"updated_at"`
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

	// Support partial updates: decode into a map to detect which fields were
	// actually present in the request, then merge into the current policy.
	var rawPatch map[string]json.RawMessage
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
	// Merge: start from the current stored policy, override only provided fields.
	policy := currentStored.Policy
	if _, ok := rawPatch["official"]; ok {
		var v bool
		if err := json.Unmarshal(rawPatch["official"], &v); err == nil {
			policy.Official = v
		}
	}
	if _, ok := rawPatch["default"]; ok {
		var v string
		if err := json.Unmarshal(rawPatch["default"], &v); err == nil {
			policy.Default = v
		}
	}
	if _, ok := rawPatch["default_reasoning_effort"]; ok {
		var v string
		if err := json.Unmarshal(rawPatch["default_reasoning_effort"], &v); err == nil {
			policy.DefaultReasoningEffort = v
		}
	}
	if _, ok := rawPatch["web_search"]; ok {
		var v string
		if err := json.Unmarshal(rawPatch["web_search"], &v); err == nil {
			policy.WebSearch = v
		}
	}
	if _, ok := rawPatch["subagents"]; ok {
		var subagentPatch map[string]json.RawMessage
		if err := json.Unmarshal(rawPatch["subagents"], &subagentPatch); err != nil {
			writeError(w, fmt.Errorf("读取子代理路由策略: %w", err), http.StatusBadRequest)
			return
		}
		if raw, present := subagentPatch["explore"]; present {
			if err := json.Unmarshal(raw, &policy.Subagents.Explore); err != nil {
				writeError(w, fmt.Errorf("读取 explore 路由: %w", err), http.StatusBadRequest)
				return
			}
		}
		if raw, present := subagentPatch["plan"]; present {
			if err := json.Unmarshal(raw, &policy.Subagents.Plan); err != nil {
				writeError(w, fmt.Errorf("读取 plan 路由: %w", err), http.StatusBadRequest)
				return
			}
		}
	}
	if policy.Official {
		if _, loggedIn := s.officialRoutingModels(); !loggedIn {
			writeError(w, fmt.Errorf("尚未登录 Grok 官方账号"), http.StatusBadRequest)
			return
		}
		if err := validateOfficialRoutingPolicy(policy); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
	}

	currentHydrated, err := routing.ProjectWithPolicy(profilesList, currentStored.Policy)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	sourceProvider, _ := providerIdentityForRouting(currentHydrated)
	hydrated, err := routing.ProjectWithPolicy(profilesList, policy)
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
	targetProvider, _ := providerIdentityForRouting(hydrated)
	var handoff *providerHandoff
	if !sameProvider(sourceProvider, targetProvider) {
		var same bool
		handoff, same, err = s.prepareAgentForProviderSwitch(targetProvider)
		if err != nil {
			writeError(w, err, http.StatusConflict)
			return
		}
		_ = same
		if handoff != nil {
			previousPolicy := currentStored.Policy
			handoff.SourceRoutingPolicy = &previousPolicy
		}
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
	if err := s.commitProviderHandoff(handoff); err != nil {
		rollbackErr := s.Switcher.RestoreConfigState(oldConfig, configExisted)
		_, routingRollbackErr := s.Routing.Replace(currentHydrated)
		if rollbackErr != nil || routingRollbackErr != nil {
			err = fmt.Errorf("%v；恢复配置失败: %v；恢复路由失败: %v", err, rollbackErr, routingRollbackErr)
		}
		writeError(w, err, http.StatusConflict)
		return
	}

	hydrated, err = routing.ProjectWithPolicy(profilesList, stored.Policy)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	hydrated.Version = stored.Version
	hydrated.UpdatedAt = stored.UpdatedAt
	s.changed()
	writeJSON(w, s.routingDTO(hydrated))
}

func (s *Server) activateProfileRouting(id string) (profiles.Profile, error) {
	if s.Routing == nil || s.Profiles == nil || s.Switcher == nil {
		return profiles.Profile{}, fmt.Errorf("路由服务未初始化")
	}
	s.routingMu.Lock()
	defer s.routingMu.Unlock()

	target, err := s.Profiles.Get(id)
	if err != nil {
		return profiles.Profile{}, err
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		return profiles.Profile{}, err
	}
	profileList, err := s.Profiles.List()
	if err != nil {
		return profiles.Profile{}, err
	}
	catalog := routing.Project(profileList)
	defaultRoute, ok := routeForProfile(catalog, target.ID, target.DefaultModel)
	if !ok {
		return profiles.Profile{}, fmt.Errorf("profile %q 的默认模型 %q 没有可用路由", target.Name, target.DefaultModel)
	}
	policy := retainValidRoutingPolicy(stored.Policy, catalog)
	policy.Official = false
	policy.Default = defaultRoute.Name
	policy.DefaultReasoningEffort = target.DefaultReasoningEffort
	if _, err := s.applyRoutingPolicyTransaction(profileList, policy); err != nil {
		return profiles.Profile{}, err
	}
	return target, nil
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
	catalog := routing.Project(profileList)
	policy := repairRoutingPolicy(stored.Policy, catalog, profileList)
	_, err = s.applyRoutingPolicyTransaction(profileList, policy)
	return err
}

func (s *Server) applyRoutingPolicyTransaction(profileList []profiles.Profile, policy routing.RoutingPolicy) (routing.Snapshot, error) {
	hydrated, err := routing.ProjectWithPolicy(profileList, policy)
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
	s.applyMCPPolicyLocked(hydrated)
	return hydrated, nil
}

// applyMCPPolicyLocked configures the Bridge's MCP servers based on the
// subagent target providers. Non-Grok subagents get browser-use injected.
// Must be called with routingMu held (or during init).
func (s *Server) applyMCPPolicyLocked(hydrated routing.Snapshot) {
	if s.Agent == nil {
		return
	}
	// Collect all subagent target models and decide whether any need browser-use.
	var servers []acp.McpServer
	for _, subagent := range []string{"explore", "plan"} {
		servers = append(servers, McpServersForSubagent(hydrated, subagent)...)
	}
	// Also consider the main session's web_search target.
	servers = append(servers, McpServersForMain(hydrated)...)
	s.Agent.SetMcpServers(dedupeMCPServers(servers))
}

// dedupeMCPServers removes duplicate MCP servers by name.
func dedupeMCPServers(servers []acp.McpServer) []acp.McpServer {
	seen := make(map[string]bool, len(servers))
	out := make([]acp.McpServer, 0, len(servers))
	for _, s := range servers {
		name := ""
		if s.Stdio != nil {
			name = s.Stdio.Name
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, s)
	}
	return out
}

func hydratedRouteProviderID(snapshot routing.Snapshot) string {
	route, ok := snapshot.Route(snapshot.Policy.Default)
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

func retainValidRoutingPolicy(policy routing.RoutingPolicy, catalog routing.Snapshot) routing.RoutingPolicy {
	if policy.Official {
		return policy
	}
	valid := func(name string) string {
		if name == "" {
			return ""
		}
		if _, ok := catalog.Route(name); ok {
			return name
		}
		return ""
	}
	policy.Default = valid(policy.Default)
	policy.WebSearch = valid(policy.WebSearch)
	policy.Subagents.Explore = valid(policy.Subagents.Explore)
	policy.Subagents.Plan = valid(policy.Subagents.Plan)
	return policy
}

func repairRoutingPolicy(policy routing.RoutingPolicy, catalog routing.Snapshot, profileList []profiles.Profile) routing.RoutingPolicy {
	if policy.Official {
		return policy
	}
	policy = retainValidRoutingPolicy(policy, catalog)
	if policy.Default != "" {
		return policy
	}
	if len(catalog.ModelRoutes) > 0 {
		policy.Default = catalog.ModelRoutes[0].Name
	}
	if len(catalog.ModelRoutes) > 0 {
		policy.Default = catalog.ModelRoutes[0].Name
	}
	return policy
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
	catalog := routing.Project(profilesList)
	policy := repairRoutingPolicy(stored.Policy, catalog, profilesList)
	if policy != stored.Policy {
		if _, err := s.applyRoutingPolicyTransaction(profilesList, policy); err != nil {
			return routingSnapshotDTO{}, routing.Snapshot{}, err
		}
		stored, err = s.Routing.Snapshot()
		if err != nil {
			return routingSnapshotDTO{}, routing.Snapshot{}, err
		}
	}
	hydrated, err := routing.ProjectWithPolicy(profilesList, stored.Policy)
	if err != nil {
		return routingSnapshotDTO{}, routing.Snapshot{}, err
	}
	hydrated.Version = stored.Version
	hydrated.UpdatedAt = stored.UpdatedAt
	return s.routingDTO(hydrated), hydrated, nil
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
	command, _, browserUseAvailable := BrowserUseCommand()
	browserUseStatus := browserUseStatusDTO{Available: browserUseAvailable, Command: command}
	if !browserUseAvailable {
		browserUseStatus.Error = "未找到 browser-use MCP 可执行文件；需要搜索回退的路由将不会获得 web_search/web_fetch 工具"
	}
	return routingSnapshotDTO{
		Version: snapshot.Version, Providers: providers, ModelRoutes: models,
		OfficialModels: officialModels, OfficialLoggedIn: officialLoggedIn,
		Policy: snapshot.Policy, WebSearchCapable: snapshot.Policy.WebSearchCapable,
		BrowserUse: browserUseStatus, UpdatedAt: snapshot.UpdatedAt,
	}
}

func validateRoutingReasoningEffort(snapshot routing.Snapshot) error {
	effort := strings.TrimSpace(snapshot.Policy.DefaultReasoningEffort)
	if effort == "" {
		return nil
	}
	if snapshot.Policy.Official {
		for _, model := range defaultOfficialRoutingModels {
			if model.Name == snapshot.Policy.Default {
				if containsReasoningEffort(model.ReasoningEfforts, effort) {
					return nil
				}
				return fmt.Errorf("官方模型 %q 不支持推理强度 %q；可用档位：%s", model.Name, effort, strings.Join(model.ReasoningEfforts, "、"))
			}
		}
		return fmt.Errorf("官方默认模型 %q 不可用", snapshot.Policy.Default)
	}
	route, ok := snapshot.Route(snapshot.Policy.Default)
	if !ok {
		return fmt.Errorf("默认路由模型 %q 不可用", snapshot.Policy.Default)
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

func (s *Server) officialRoutingModels() ([]routingModelDTO, bool) {
	if s.Paths.GrokHome == "" {
		return nil, false
	}
	if _, err := os.Stat(filepath.Join(s.Paths.GrokHome, "auth.json")); err != nil {
		return nil, false
	}
	models := make([]routingModelDTO, 0, len(defaultOfficialRoutingModels))
	for _, model := range defaultOfficialRoutingModels {
		models = append(models, routingModelDTO{
			ID: model.Name, Name: model.Name, ProviderID: "official", ProfileModel: model.Name,
			Model: model.Model, APIBackend: "official",
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
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(r.Body)
	// When decoding into a map (partial update), allow unknown fields so the
	// frontend can send a subset of policy fields.
	if _, isMap := out.(*map[string]json.RawMessage); !isMap {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("请求格式无效: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("请求格式无效")
	}
	return nil
}
