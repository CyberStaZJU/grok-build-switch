package routing

import (
	"fmt"
	"strings"
	"time"

	"grok_switch/internal/profiles"
)

const (
	CurrentVersion     = 2
	OfficialProviderID = "official"
)

type Provider struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ProfileID string `json:"profile_id"`
	Source    string `json:"source,omitempty"`

	UpstreamFormat string `json:"-"`
	BaseURL        string `json:"-"`
	APIKey         string `json:"-"`
}

type ModelRoute struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ProviderID     string `json:"provider_id"`
	ProfileModel   string `json:"profile_model"`
	SpeedTier      string `json:"speed_tier,omitempty"`
	StandardAnchor string `json:"standard_anchor,omitempty"`

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

// RoutingPolicy contains one provider's remembered route choices. Custom
// policies use stable ModelRoute IDs; the special official policy uses official
// model IDs because official models have no custom catalog entries.
type RoutingPolicy struct {
	Default                string          `json:"default,omitempty"`
	DefaultReasoningEffort string          `json:"default_reasoning_effort,omitempty"`
	WebSearch              string          `json:"web_search,omitempty"`
	WebSearchCapable       bool            `json:"web_search_capable"`
	Subagents              SubagentsPolicy `json:"subagents,omitempty"`

	// Official is accepted only while migrating schema v1 and is never emitted.
	Official bool `json:"-"`
}

type Snapshot struct {
	Version          int                      `json:"version"`
	ActiveProviderID string                   `json:"active_provider_id,omitempty"`
	Providers        []Provider               `json:"providers"`
	ModelRoutes      []ModelRoute             `json:"model_routes"`
	ProviderPolicies map[string]RoutingPolicy `json:"provider_policies"`
	UpdatedAt        time.Time                `json:"updated_at"`

	// Policy mirrors the active provider policy for internal compatibility.
	// It is derived from ProviderPolicies and never serialized.
	Policy   RoutingPolicy `json:"-"`
	Hydrated bool          `json:"-"`
}

func (s Snapshot) Provider(id string) (Provider, bool) {
	for _, provider := range s.Providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

func (s Snapshot) Route(ref string) (ModelRoute, bool) {
	for _, route := range s.ModelRoutes {
		if route.ID == ref || route.Name == ref {
			return route, true
		}
	}
	return ModelRoute{}, false
}

func routeByExactID(s Snapshot, id string) (ModelRoute, bool) {
	for _, route := range s.ModelRoutes {
		if route.ID == id {
			return route, true
		}
	}
	return ModelRoute{}, false
}

func (s Snapshot) ActivePolicy() RoutingPolicy {
	if !policyEmpty(s.Policy) || s.Policy.Official {
		return s.Policy
	}
	if policy, ok := s.ProviderPolicies[s.ActiveProviderID]; ok {
		return policy
	}
	return RoutingPolicy{}
}

func (s Snapshot) IsOfficial() bool {
	return s.ActiveProviderID == OfficialProviderID || s.Policy.Official
}

func (s Snapshot) RoutesForProvider(providerID string) []ModelRoute {
	out := make([]ModelRoute, 0)
	for _, route := range s.ModelRoutes {
		if route.ProviderID == providerID {
			out = append(out, route)
		}
	}
	return out
}

func (s Snapshot) WebSearchCapable() bool {
	policy := s.ActivePolicy()
	if s.IsOfficial() || policy.WebSearch == "" {
		return true
	}
	route, ok := s.Route(policy.WebSearch)
	return ok && (s.ActiveProviderID == "" || route.ProviderID == s.ActiveProviderID) && route.APIBackend == "responses" && route.SupportsBackendSearch
}

func (s Snapshot) SubagentWebSearchCapable(subagent string) bool {
	policy := s.ActivePolicy()
	name := ""
	switch subagent {
	case "explore":
		name = policy.Subagents.Explore
	case "plan":
		name = policy.Subagents.Plan
	}
	if name == "" || s.IsOfficial() {
		return true
	}
	route, ok := s.Route(name)
	return ok && (s.ActiveProviderID == "" || route.ProviderID == s.ActiveProviderID) && route.APIBackend == "responses" && route.SupportsBackendSearch
}

func (s Snapshot) Validate() error {
	providers := make(map[string]bool, len(s.Providers))
	for _, provider := range s.Providers {
		if strings.TrimSpace(provider.ID) == "" || provider.ID == OfficialProviderID {
			return fmt.Errorf("invalid routing provider id %q", provider.ID)
		}
		if providers[provider.ID] {
			return fmt.Errorf("duplicate routing provider id %q", provider.ID)
		}
		providers[provider.ID] = true
	}
	routes := make(map[string]ModelRoute, len(s.ModelRoutes))
	names := make(map[string]bool, len(s.ModelRoutes))
	for _, route := range s.ModelRoutes {
		if strings.TrimSpace(route.ID) == "" || strings.TrimSpace(route.Name) == "" {
			return fmt.Errorf("routing model id or name is empty")
		}
		if _, exists := routes[route.ID]; exists {
			return fmt.Errorf("duplicate routing model id %q", route.ID)
		}
		if names[route.Name] {
			return fmt.Errorf("duplicate routing model name %q", route.Name)
		}
		if !providers[route.ProviderID] {
			return fmt.Errorf("routing model %q references unknown provider %q", route.Name, route.ProviderID)
		}
		routes[route.ID] = route
		names[route.Name] = true
	}
	if len(providers) > 0 && s.ActiveProviderID == "" {
		return fmt.Errorf("active provider is required")
	}
	if s.ActiveProviderID != "" && s.ActiveProviderID != OfficialProviderID && !providers[s.ActiveProviderID] {
		return fmt.Errorf("active provider %q is unavailable", s.ActiveProviderID)
	}
	for _, route := range s.ModelRoutes {
		tier := strings.TrimSpace(route.SpeedTier)
		anchorID := strings.TrimSpace(route.StandardAnchor)
		if (tier == "") != (anchorID == "") {
			return fmt.Errorf("routing model %q speed_tier and standard_anchor must both be present or both be absent", route.ID)
		}
		if tier == "" {
			continue
		}
		if route.SpeedTier != tier || route.StandardAnchor != anchorID {
			return fmt.Errorf("routing model %q speed metadata must not contain surrounding whitespace", route.ID)
		}
		switch tier {
		case profiles.SpeedTierStandard:
			if anchorID != route.ID {
				return fmt.Errorf("standard routing model %q must self-anchor", route.ID)
			}
		case profiles.SpeedTierFast:
			anchor, ok := routes[anchorID]
			if !ok {
				return fmt.Errorf("fast routing model %q references unknown standard anchor %q", route.ID, anchorID)
			}
			if anchor.ProviderID != route.ProviderID {
				return fmt.Errorf("fast routing model %q and standard anchor %q must use the same provider", route.ID, anchorID)
			}
			if anchor.SpeedTier != profiles.SpeedTierStandard || anchor.StandardAnchor != anchor.ID {
				return fmt.Errorf("fast routing model %q anchor %q is not an explicit standard route", route.ID, anchorID)
			}
		default:
			return fmt.Errorf("routing model %q has invalid speed tier %q", route.ID, tier)
		}
	}
	for providerID, policy := range s.ProviderPolicies {
		if providerID == "" {
			return fmt.Errorf("policy references empty provider")
		}
		if providerID != OfficialProviderID && !providers[providerID] {
			return fmt.Errorf("policy references unknown provider %q", providerID)
		}
		if providerID == OfficialProviderID {
			continue
		}
		for label, ref := range map[string]string{"default": policy.Default, "web_search": policy.WebSearch, "subagents.explore": policy.Subagents.Explore, "subagents.plan": policy.Subagents.Plan} {
			if ref == "" {
				continue
			}
			route, ok := routes[ref]
			if !ok {
				return fmt.Errorf("routing policy %s references unknown model %q", label, ref)
			}
			if route.ProviderID != providerID {
				return fmt.Errorf("routing policy %s model %q belongs to provider %q, not active provider %q", label, ref, route.ProviderID, providerID)
			}
		}
	}
	if s.ActiveProviderID != "" {
		policy, ok := s.ProviderPolicies[s.ActiveProviderID]
		if !ok {
			return fmt.Errorf("active provider %q has no remembered policy", s.ActiveProviderID)
		}
		if policy.Default == "" {
			return fmt.Errorf("active provider %q has no default model", s.ActiveProviderID)
		}
	}
	return nil
}

// Project creates stable provider/model identities and deterministic aliases.
func Project(source []profiles.Profile) Snapshot {
	items := append([]profiles.Profile(nil), source...)
	providerNames := uniqueProviderNames(items)
	nameCounts := map[string]int{}
	for _, original := range items {
		for _, model := range profiles.Normalize(original).Models {
			nameCounts[modelName(model)]++
		}
	}
	snapshot := Snapshot{Version: CurrentVersion, UpdatedAt: time.Now().UTC(), ProviderPolicies: map[string]RoutingPolicy{}}
	usedRouteNames := map[string]int{}
	for i, original := range items {
		profile := profiles.Normalize(original)
		providerID := profile.ID
		if providerID == "" {
			providerID = fmt.Sprintf("profile-%d", i+1)
		}
		provider := Provider{ID: providerID, Name: providerNames[i], ProfileID: profile.ID, Source: profile.Source}
		snapshot.Providers = append(snapshot.Providers, provider)
		aliasRouteIDs := make(map[string]string, len(profile.Models))
		start := len(snapshot.ModelRoutes)
		for modelIndex, model := range profile.Models {
			localName := modelName(model)
			if localName == "" {
				localName = fmt.Sprintf("model-%d", modelIndex+1)
			}
			routeName := localName
			if nameCounts[localName] > 1 {
				routeName += "@" + provider.Name
			}
			usedRouteNames[routeName]++
			if usedRouteNames[routeName] > 1 {
				routeName = fmt.Sprintf("%s (%d)", routeName, usedRouteNames[routeName])
			}
			routeID := providerID + ":" + localName
			aliasRouteIDs[localName] = routeID
			snapshot.ModelRoutes = append(snapshot.ModelRoutes, ModelRoute{
				ID: routeID, Name: routeName, ProviderID: providerID, ProfileModel: localName,
				SpeedTier:             model.SpeedTier,
				SupportsBackendSearch: model.SupportsBackendSearch, SupportsReasoningEffort: model.SupportsReasoningEffort,
				ReasoningEfforts: append([]string(nil), model.ReasoningEfforts...), ReasoningEffortsSource: model.ReasoningEffortsSource,
				ContextWindow: model.ContextWindow, MaxCompletionTokens: model.MaxCompletionTokens,
			})
		}
		for routeIndex, model := range profile.Models {
			anchor := strings.TrimSpace(model.StandardAnchor)
			if anchor == "" {
				continue
			}
			snapshot.ModelRoutes[start+routeIndex].StandardAnchor = aliasRouteIDs[anchor]
		}
		policy := defaultPolicyForProvider(snapshot, providerID, profile.DefaultModel, profile.DefaultReasoningEffort)
		snapshot.ProviderPolicies[providerID] = policy
		if snapshot.ActiveProviderID == "" && policy.Default != "" {
			snapshot.ActiveProviderID = providerID
		}
	}
	snapshot.Policy = snapshot.ProviderPolicies[snapshot.ActiveProviderID]
	return snapshot
}

func defaultPolicyForProvider(snapshot Snapshot, providerID, preferredModel, effort string) RoutingPolicy {
	var selected ModelRoute
	for _, route := range snapshot.ModelRoutes {
		if route.ProviderID != providerID {
			continue
		}
		if selected.ID == "" {
			selected = route
		}
		if route.ProfileModel == preferredModel {
			selected = route
			break
		}
	}
	policy := RoutingPolicy{Default: selected.ID, DefaultReasoningEffort: effort}
	if policy.DefaultReasoningEffort == "" && selected.SupportsReasoningEffort && containsReasoningEffort(selected.ReasoningEfforts, "high") {
		policy.DefaultReasoningEffort = "high"
	}
	return policy
}

// ProjectWithSnapshot rebuilds the catalog while retaining every provider's
// remembered policy. Removed/renamed model references are repaired only within
// that provider; policies never cross provider boundaries.
func ProjectWithPolicy(source []profiles.Profile, policy RoutingPolicy) (Snapshot, error) {
	base := Project(source)
	active := ""
	if route, ok := base.Route(policy.Default); ok {
		active = route.ProviderID
	}
	if active == "" {
		for _, ref := range []string{policy.WebSearch, policy.Subagents.Explore, policy.Subagents.Plan} {
			if route, ok := base.Route(ref); ok {
				active = route.ProviderID
				break
			}
		}
	}
	if policy.Official {
		active = OfficialProviderID
	}
	if active == "" {
		active = base.ActiveProviderID
	}
	previous := base
	previous.ActiveProviderID = active
	if previous.ProviderPolicies == nil {
		previous.ProviderPolicies = map[string]RoutingPolicy{}
	}
	translated := policy
	translated.Official = false
	if active != OfficialProviderID {
		for field, ref := range map[string]string{"default": policy.Default, "web_search": policy.WebSearch, "explore": policy.Subagents.Explore, "plan": policy.Subagents.Plan} {
			if route, ok := base.Route(ref); ok {
				switch field {
				case "default":
					translated.Default = route.ID
				case "web_search":
					translated.WebSearch = route.ID
				case "explore":
					translated.Subagents.Explore = route.ID
				case "plan":
					translated.Subagents.Plan = route.ID
				}
			}
		}
	}
	previous.ProviderPolicies[active] = translated
	return ProjectWithSnapshot(source, previous)
}

// RepairPolicy is kept for callers migrating from schema v1. It returns the
// active provider's repaired policy; new code should retain the full snapshot.
func RepairPolicy(source []profiles.Profile, policy RoutingPolicy) RoutingPolicy {
	snapshot, err := ProjectWithPolicy(source, policy)
	if err != nil {
		return policy
	}
	out := snapshot.ActivePolicy()
	out.Official = snapshot.IsOfficial()
	return out
}

func repairWebSearch(name string, catalog Snapshot) string {
	if route, ok := catalog.Route(name); ok {
		if strings.TrimSpace(route.ID) != "" {
			return route.ID
		}
		return route.Name
	}
	return ""
}

func ProjectWithSnapshot(source []profiles.Profile, previous Snapshot) (Snapshot, error) {
	snapshot := Project(source)
	for providerID, defaults := range snapshot.ProviderPolicies {
		if remembered, ok := previous.ProviderPolicies[providerID]; ok {
			snapshot.ProviderPolicies[providerID] = repairProviderPolicy(snapshot, providerID, remembered, defaults)
		}
	}
	if official, ok := previous.ProviderPolicies[OfficialProviderID]; ok {
		snapshot.ProviderPolicies[OfficialProviderID] = official
	}
	snapshot.ActiveProviderID = previous.ActiveProviderID
	if snapshot.ActiveProviderID != OfficialProviderID {
		if _, ok := snapshot.Provider(snapshot.ActiveProviderID); !ok {
			snapshot.ActiveProviderID = firstUsableProvider(snapshot)
		}
	}
	if snapshot.ActiveProviderID == "" && len(snapshot.Providers) > 0 {
		snapshot.ActiveProviderID = firstUsableProvider(snapshot)
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	hydrated, err := Hydrate(snapshot, source)
	if err != nil {
		return Snapshot{}, err
	}
	hydrated.Policy = hydrated.ProviderPolicies[hydrated.ActiveProviderID]
	hydrated.Policy.Official = hydrated.IsOfficial()
	policy := hydrated.ActivePolicy()
	policy.WebSearchCapable = hydrated.WebSearchCapable()
	if hydrated.ActiveProviderID != "" {
		hydrated.ProviderPolicies[hydrated.ActiveProviderID] = policy
	}
	hydrated.Policy = policy
	hydrated.Policy.Official = hydrated.IsOfficial()
	return hydrated, nil
}

// RepairUnsupportedWebSearch clears legacy custom web_search selections that
// cannot satisfy the schema-v2 backend contract. It is intended for explicit
// startup migration, not interactive policy updates, which must reject invalid
// selections instead of silently changing them.
func RepairUnsupportedWebSearch(snapshot Snapshot) (Snapshot, bool) {
	out := cloneSnapshot(snapshot)
	changed := false
	for providerID, policy := range out.ProviderPolicies {
		if providerID == OfficialProviderID || strings.TrimSpace(policy.WebSearch) == "" {
			continue
		}
		route, ok := out.Route(policy.WebSearch)
		if !ok || route.ProviderID != providerID || route.APIBackend != "responses" || !route.SupportsBackendSearch {
			policy.WebSearch = ""
			policy.WebSearchCapable = true
			out.ProviderPolicies[providerID] = policy
			changed = true
		}
	}
	out.Policy = out.ProviderPolicies[out.ActiveProviderID]
	out.Policy.Official = out.IsOfficial()
	return out, changed
}

func repairProviderPolicy(snapshot Snapshot, providerID string, policy, defaults RoutingPolicy) RoutingPolicy {
	valid := func(ref string) string {
		route, ok := snapshot.Route(ref)
		if ok && route.ProviderID == providerID {
			return route.ID
		}
		return ""
	}
	policy.Default = valid(policy.Default)
	policy.WebSearch = valid(policy.WebSearch)
	policy.Subagents.Explore = valid(policy.Subagents.Explore)
	policy.Subagents.Plan = valid(policy.Subagents.Plan)
	if policy.Default == "" {
		policy.Default = defaults.Default
		policy.DefaultReasoningEffort = defaults.DefaultReasoningEffort
	}
	return policy
}

func firstUsableProvider(snapshot Snapshot) string {
	for _, provider := range snapshot.Providers {
		if snapshot.ProviderPolicies[provider.ID].Default != "" {
			return provider.ID
		}
	}
	return ""
}

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
		out.Providers[i].UpstreamFormat, out.Providers[i].BaseURL, out.Providers[i].APIKey = profile.UpstreamFormat, profile.BaseURL, profile.EffectiveAPIKey()
	}
	for i := range out.ModelRoutes {
		route := &out.ModelRoutes[i]
		provider, _ := out.Provider(route.ProviderID)
		profile := byID[provider.ProfileID]
		model, ok := profileModel(profile, route.ProfileModel)
		if !ok {
			return Snapshot{}, fmt.Errorf("routing model %q references missing profile model %q", route.Name, route.ProfileModel)
		}
		route.Model, route.APIBackend = model.Model, model.APIBackend
		route.BaseURL, route.APIKey = firstNonEmpty(model.BaseURL, provider.BaseURL), firstNonEmpty(model.APIKey, provider.APIKey)
		route.ExtraHeaders = cloneMap(model.ExtraHeaders)
		route.SupportsBackendSearch, route.SupportsReasoningEffort = model.SupportsBackendSearch, model.SupportsReasoningEffort
		route.ReasoningEfforts, route.ReasoningEffortsSource = append([]string(nil), model.ReasoningEfforts...), model.ReasoningEffortsSource
		route.ContextWindow, route.MaxCompletionTokens = model.ContextWindow, model.MaxCompletionTokens
		if route.SpeedTier != model.SpeedTier {
			return Snapshot{}, fmt.Errorf("routing model %q speed tier no longer matches profile metadata", route.Name)
		}
		if model.StandardAnchor == "" {
			if route.StandardAnchor != "" {
				return Snapshot{}, fmt.Errorf("routing model %q standard anchor no longer matches profile metadata", route.Name)
			}
		} else {
			anchorRoute, ok := routeForProfileModel(out, route.ProviderID, model.StandardAnchor)
			if !ok || route.StandardAnchor != anchorRoute.ID {
				return Snapshot{}, fmt.Errorf("routing model %q standard anchor no longer matches profile metadata", route.Name)
			}
		}
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

func routeForProfileModel(snapshot Snapshot, providerID, profileModel string) (ModelRoute, bool) {
	for _, route := range snapshot.ModelRoutes {
		if route.ProviderID == providerID && route.ProfileModel == profileModel {
			return route, true
		}
	}
	return ModelRoute{}, false
}

func policyEmpty(policy RoutingPolicy) bool {
	return !policy.Official && policy.Default == "" && policy.DefaultReasoningEffort == "" && policy.WebSearch == "" && policy.Subagents.Explore == "" && policy.Subagents.Plan == ""
}

func containsReasoningEffort(efforts []string, target string) bool {
	for _, effort := range efforts {
		if effort == target {
			return true
		}
	}
	return false
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
