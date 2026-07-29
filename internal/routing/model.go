package routing

import (
	"fmt"
	"strings"
	"time"

	"grok_switch/internal/profiles"
)

const CurrentVersion = 1

type Provider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProfileID string `json:"profile_id"`
	Source    string `json:"source,omitempty"`

	// Runtime fields are hydrated from the latest profiles and are never
	// serialized to routing.json or exposed by the default JSON API.
	UpstreamFormat string `json:"-"`
	BaseURL        string `json:"-"`
	APIKey         string `json:"-"`
}

type ModelRoute struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderID   string `json:"provider_id"`
	ProfileModel string `json:"profile_model"`

	// Runtime fields are hydrated from the latest profiles. In particular,
	// credentials and headers must never become part of persistent routing state.
	Model                   string            `json:"-"`
	APIBackend              string            `json:"-"`
	BaseURL                 string            `json:"-"`
	APIKey                  string            `json:"-"`
	ExtraHeaders            map[string]string `json:"-"`
	SupportsBackendSearch   bool              `json:"supports_backend_search"`
	SupportsReasoningEffort bool              `json:"supports_reasoning_effort"`
	ReasoningEfforts        []string          `json:"reasoning_efforts,omitempty"`
	ReasoningEffortsSource  string            `json:"reasoning_efforts_source,omitempty"`
	ContextWindow           int64             `json:"context_window,omitempty"`
	MaxCompletionTokens     int64             `json:"max_completion_tokens,omitempty"`
}

type SubagentsPolicy struct {
	Explore string `json:"explore,omitempty"`
	Plan    string `json:"plan,omitempty"`
}

type RoutingPolicy struct {
	Official               bool            `json:"official,omitempty"`
	Default                string          `json:"default,omitempty"`
	DefaultReasoningEffort string          `json:"default_reasoning_effort,omitempty"`
	WebSearch              string          `json:"web_search,omitempty"`
	WebSearchCapable       bool            `json:"web_search_capable"`
	Subagents              SubagentsPolicy `json:"subagents,omitempty"`
}

type Snapshot struct {
	Version     int           `json:"version"`
	Providers   []Provider    `json:"providers"`
	ModelRoutes []ModelRoute  `json:"model_routes"`
	Policy      RoutingPolicy `json:"policy"`
	UpdatedAt   time.Time     `json:"updated_at"`

	Hydrated bool `json:"-"`
}

func (s Snapshot) Provider(id string) (Provider, bool) {
	for _, provider := range s.Providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

func (s Snapshot) Route(name string) (ModelRoute, bool) {
	for _, route := range s.ModelRoutes {
		if route.Name == name || route.ID == name {
			return route, true
		}
	}
	return ModelRoute{}, false
}

// WebSearchCapable 判断当前 web_search 路由目标模型是否支持 x_search 工具。
// 仅可在 Hydrated 快照上调用：需要 APIBackend 与 SupportsBackendSearch 已填充。
// 官方模式或 web_search 为空时返回 true（官方模型原生支持，空则不参与判断）。
func (s Snapshot) WebSearchCapable() bool {
	if s.Policy.Official || s.Policy.WebSearch == "" {
		return true
	}
	route, ok := s.Route(s.Policy.WebSearch)
	if !ok {
		return false
	}
	return route.APIBackend == "responses" && route.SupportsBackendSearch
}

// SubagentWebSearchCapable 判断指定子代理类型的目标模型是否支持 x_search 工具。
// 仅可在 Hydrated 快照上调用。子代理的空路由或官方模式返回 true（不约束）。
func (s Snapshot) SubagentWebSearchCapable(subagent string) bool {
	name := ""
	switch subagent {
	case "explore":
		name = s.Policy.Subagents.Explore
	case "plan":
		name = s.Policy.Subagents.Plan
	}
	if name == "" || s.Policy.Official {
		return true
	}
	route, ok := s.Route(name)
	if !ok {
		return false
	}
	return route.APIBackend == "responses" && route.SupportsBackendSearch
}

// repairWebSearch 保留仍存在的 web_search 路由；路由失效时清空。
// 不兼容 x_search 的模型不会被替换：系统通过 WebSearchCapable=false
// 通知 Agent 改用 browser-use，而不是擅自切换用户选择的模型。
func repairWebSearch(name string, catalog Snapshot) string {
	if name == "" {
		return ""
	}
	if _, ok := catalog.Route(name); ok {
		return name
	}
	return ""
}

func (s Snapshot) Validate() error {
	providers := make(map[string]bool, len(s.Providers))
	for _, provider := range s.Providers {
		if strings.TrimSpace(provider.ID) == "" {
			return fmt.Errorf("routing provider id is empty")
		}
		if providers[provider.ID] {
			return fmt.Errorf("duplicate routing provider id %q", provider.ID)
		}
		providers[provider.ID] = true
	}
	routes := make(map[string]bool, len(s.ModelRoutes))
	for _, route := range s.ModelRoutes {
		if strings.TrimSpace(route.Name) == "" {
			return fmt.Errorf("routing model name is empty")
		}
		if routes[route.Name] {
			return fmt.Errorf("duplicate routing model name %q", route.Name)
		}
		if !providers[route.ProviderID] {
			return fmt.Errorf("routing model %q references unknown provider %q", route.Name, route.ProviderID)
		}
		routes[route.Name] = true
	}
	if s.Policy.Official {
		return nil
	}
	for label, name := range map[string]string{
		"default":           s.Policy.Default,
		"web_search":        s.Policy.WebSearch,
		"subagents.explore": s.Policy.Subagents.Explore,
		"subagents.plan":    s.Policy.Subagents.Plan,
	} {
		if name != "" && !routes[name] {
			return fmt.Errorf("routing policy %s references unknown model %q", label, name)
		}
	}
	return nil
}

// Project converts legacy provider profiles into a deterministic multi-provider
// routing snapshot. Model names that collide are qualified with a provider name.
func Project(source []profiles.Profile) Snapshot {
	items := append([]profiles.Profile(nil), source...)
	providerNames := uniqueProviderNames(items)
	nameCounts := map[string]int{}
	for _, profile := range items {
		profile = profiles.Normalize(profile)
		for _, model := range profile.Models {
			nameCounts[modelName(model)]++
		}
	}

	snapshot := Snapshot{Version: CurrentVersion, UpdatedAt: time.Now().UTC()}
	translations := make(map[string]map[string]string, len(items))
	providerIDs := make([]string, len(items))
	usedProviderIDs := map[string]int{}
	usedRouteNames := map[string]int{}
	for i, original := range items {
		profile := profiles.Normalize(original)
		providerID := profile.ID
		if providerID == "" {
			providerID = fmt.Sprintf("profile-%d", i+1)
		}
		usedProviderIDs[providerID]++
		if usedProviderIDs[providerID] > 1 {
			providerID = fmt.Sprintf("%s-%d", providerID, usedProviderIDs[providerID])
		}
		providerIDs[i] = providerID
		provider := Provider{
			ID:        providerID,
			Name:      providerNames[i],
			ProfileID: profile.ID,
			Source:    profile.Source,
		}
		snapshot.Providers = append(snapshot.Providers, provider)
		translations[providerID] = map[string]string{}
		for modelIndex, model := range profile.Models {
			localName := modelName(model)
			routeName := localName
			if nameCounts[localName] > 1 {
				routeName = localName + "@" + provider.Name
			}
			routeID := fmt.Sprintf("%s:%s", providerID, localName)
			if localName == "" {
				routeID = fmt.Sprintf("%s:model-%d", providerID, modelIndex+1)
				routeName = fmt.Sprintf("model-%d@%s", modelIndex+1, provider.Name)
			}
			usedRouteNames[routeName]++
			if usedRouteNames[routeName] > 1 {
				routeName = fmt.Sprintf("%s (%d)", routeName, usedRouteNames[routeName])
			}
			translations[providerID][localName] = routeName
			snapshot.ModelRoutes = append(snapshot.ModelRoutes, ModelRoute{
				ID:                      routeID,
				Name:                    routeName,
				ProviderID:              providerID,
				ProfileModel:            localName,
				SupportsBackendSearch:   model.SupportsBackendSearch,
				SupportsReasoningEffort: model.SupportsReasoningEffort,
				ReasoningEfforts:        append([]string(nil), model.ReasoningEfforts...),
				ReasoningEffortsSource:  model.ReasoningEffortsSource,
				ContextWindow:           model.ContextWindow,
				MaxCompletionTokens:     model.MaxCompletionTokens,
			})
		}
	}
	// With routing as the single source of truth, there is no "active profile".
	// Default to the first available route so the routing policy is never empty.
	if len(snapshot.ModelRoutes) > 0 {
		snapshot.Policy = RoutingPolicy{
			Default:                snapshot.ModelRoutes[0].Name,
			DefaultReasoningEffort: "high",
		}
	}
	return snapshot
}

// ProjectWithPolicy rebuilds the routing catalog from current profiles while
// retaining a previously selected policy whenever all referenced routes still
// exist. The returned snapshot is hydrated in memory for config generation.
func ProjectWithPolicy(source []profiles.Profile, policy RoutingPolicy) (Snapshot, error) {
	snapshot := Project(source)
	if !policyEmpty(policy) {
		snapshot.Policy = policy
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	hydrated, err := Hydrate(snapshot, source)
	if err != nil {
		return Snapshot{}, err
	}
	hydrated.Policy.WebSearchCapable = hydrated.WebSearchCapable()
	return hydrated, nil
}

// RepairPolicy retains routes that still exist after profile changes, clears
// invalid optional routes, and chooses the active profile's default (or the
// first available route) when the previous default disappeared.
// web_search 路由失效或指向不支持 x_search 的模型时，自动回退到支持
// responses+backend_search 的模型；找不到时清空。
func RepairPolicy(source []profiles.Profile, policy RoutingPolicy) RoutingPolicy {
	if policy.Official {
		return policy
	}
	catalog := Project(source)
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
	policy.WebSearch = repairWebSearch(policy.WebSearch, catalog)
	policy.Subagents.Explore = valid(policy.Subagents.Explore)
	policy.Subagents.Plan = valid(policy.Subagents.Plan)
	if policy.Default != "" {
		return policy
	}
	if len(catalog.ModelRoutes) > 0 {
		policy.Default = catalog.ModelRoutes[0].Name
	}
	return policy
}

// Hydrate injects runtime endpoints, credentials, headers, and concrete model
// metadata from the latest profile list into a detached in-memory snapshot.
// None of the injected fields participate in JSON serialization.
func Hydrate(snapshot Snapshot, source []profiles.Profile) (Snapshot, error) {
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	byID := make(map[string]profiles.Profile, len(source))
	for _, profile := range source {
		if profile.ID != "" {
			byID[profile.ID] = profiles.Normalize(profile)
		}
	}
	out := cloneSnapshot(snapshot)
	for i := range out.Providers {
		profile, ok := byID[out.Providers[i].ProfileID]
		if !ok {
			return Snapshot{}, fmt.Errorf("routing provider %q references missing profile %q", out.Providers[i].Name, out.Providers[i].ProfileID)
		}
		out.Providers[i].UpstreamFormat = profile.UpstreamFormat
		out.Providers[i].BaseURL = profile.BaseURL
		out.Providers[i].APIKey = profile.EffectiveAPIKey()
	}
	for i := range out.ModelRoutes {
		route := &out.ModelRoutes[i]
		provider, ok := out.Provider(route.ProviderID)
		if !ok {
			return Snapshot{}, fmt.Errorf("routing model %q references missing provider %q", route.Name, route.ProviderID)
		}
		profile := byID[provider.ProfileID]
		model, ok := profileModel(profile, route.ProfileModel)
		if !ok {
			return Snapshot{}, fmt.Errorf("routing model %q references missing profile model %q", route.Name, route.ProfileModel)
		}
		route.Model = model.Model
		route.APIBackend = model.APIBackend
		route.BaseURL = firstNonEmpty(model.BaseURL, provider.BaseURL)
		route.APIKey = firstNonEmpty(model.APIKey, provider.APIKey)
		route.ExtraHeaders = cloneMap(model.ExtraHeaders)
		route.SupportsBackendSearch = model.SupportsBackendSearch
		route.SupportsReasoningEffort = model.SupportsReasoningEffort
		route.ReasoningEfforts = append([]string(nil), model.ReasoningEfforts...)
		route.ReasoningEffortsSource = model.ReasoningEffortsSource
		route.ContextWindow = model.ContextWindow
		route.MaxCompletionTokens = model.MaxCompletionTokens
	}
	out.Hydrated = true
	return out, nil
}

func profileModel(profile profiles.Profile, name string) (profiles.ModelDef, bool) {
	for _, model := range profile.Models {
		if modelName(model) == name {
			return model, true
		}
	}
	return profiles.ModelDef{}, false
}

func policyEmpty(policy RoutingPolicy) bool {
	return !policy.Official && policy.Default == "" && policy.DefaultReasoningEffort == "" && policy.WebSearch == "" && policy.Subagents.Explore == "" && policy.Subagents.Plan == ""
}

func modelName(model profiles.ModelDef) string {
	if strings.TrimSpace(model.Name) != "" {
		return strings.TrimSpace(model.Name)
	}
	return strings.TrimSpace(model.Model)
}

func uniqueProviderNames(items []profiles.Profile) []string {
	used := map[string]int{}
	out := make([]string, len(items))
	for i, profile := range items {
		base := strings.TrimSpace(profile.Name)
		if base == "" {
			base = "Provider"
		}
		used[base]++
		out[i] = base
		if used[base] > 1 {
			out[i] = fmt.Sprintf("%s (%d)", base, used[base])
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cloneMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
