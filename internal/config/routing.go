package config

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
)

// ProfileForRouting composes every provider model into the legacy config shape
// used by the TOML writer. Route names are already conflict-free aliases.
// Note: web_search and subagents.models are NOT stored on the profile — they
// are owned exclusively by the routing policy (see applyRoutingPolicyToDoc).
func ProfileForRouting(snapshot routing.Snapshot) (profiles.Profile, error) {
	if err := snapshot.Validate(); err != nil {
		return profiles.Profile{}, err
	}
	if !snapshot.Hydrated {
		return profiles.Profile{}, fmt.Errorf("routing snapshot is not hydrated from current profiles")
	}
	policy := snapshot.ActivePolicy()
	defaultRoute, _ := snapshot.Route(policy.Default)
	policy = policyWithConfigAliases(snapshot)
	profile := profiles.Profile{
		Name:                   "Routing",
		DefaultModel:           defaultRoute.Name,
		DefaultReasoningEffort: policy.DefaultReasoningEffort,
		Models:                 make([]profiles.ModelDef, 0, len(snapshot.ModelRoutes)),
	}
	// Keep the complete custom model catalog for old conversations pinned to
	// aliases from previously active custom providers. Only auth/routing pins are
	// single-provider; model definitions remain combined until official is active.
	for _, route := range snapshot.ModelRoutes {
		provider, _ := snapshot.Provider(route.ProviderID)
		profile.Models = append(profile.Models, profiles.ModelDef{
			Name:                    route.Name,
			Model:                   route.Model,
			BaseURL:                 firstNonEmptyRouting(route.BaseURL, provider.BaseURL),
			APIKey:                  firstNonEmptyRouting(route.APIKey, provider.APIKey),
			APIBackend:              firstNonEmptyRouting(route.APIBackend, profiles.APIBackendForUpstreamFormat(provider.UpstreamFormat)),
			ExtraHeaders:            cloneRoutingHeaders(route.ExtraHeaders),
			SupportsBackendSearch:   route.SupportsBackendSearch,
			SupportsReasoningEffort: route.SupportsReasoningEffort,
			ReasoningEfforts:        append([]string(nil), route.ReasoningEfforts...),
			ReasoningEffortsSource:  route.ReasoningEffortsSource,
			SpeedTier:               route.SpeedTier,
			StandardAnchor:          profileAnchorAlias(snapshot, route),
			ContextWindow:           route.ContextWindow,
			MaxCompletionTokens:     route.MaxCompletionTokens,
		})
	}
	// Per-model base_url is authoritative for a multi-provider config. Keep the
	// legacy global endpoint aligned with the default route for compatibility.
	if route, ok := snapshot.Route(policy.Default); ok {
		provider, _ := snapshot.Provider(route.ProviderID)
		profile.BaseURL = firstNonEmptyRouting(route.BaseURL, provider.BaseURL)
	}
	profile = profiles.Normalize(profile)
	if err := profiles.ValidateEndpoints(profile); err != nil {
		return profiles.Profile{}, err
	}
	return profile, nil
}

func ApplyRoutingToFile(path string, snapshot routing.Snapshot) error {
	if snapshot.IsOfficial() {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return atomicWrite(path, ApplyOfficialRoutingText(data, snapshot.ActivePolicy()))
	}
	profile, err := ProfileForRouting(snapshot)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next, err := ApplyProfileText(data, profile)
	if err != nil {
		return err
	}
	// Layer routing-policy-managed keys (web_search, subagents.models) on top of
	// the profile-owned sections. Routing policy is the single source of truth.
	policy := policyWithConfigAliases(snapshot)
	if policyLines := rewriteRoutingPolicySections(splitLines(string(next)), policy); len(policyLines) > 0 {
		next = []byte(strings.TrimRight(strings.Join(policyLines, "\n"), "\n") + "\n")
	}
	return atomicWrite(path, next)
}

func PreviewRouting(path string, snapshot routing.Snapshot) ([]byte, error) {
	if snapshot.IsOfficial() {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return ApplyOfficialRoutingText(data, snapshot.ActivePolicy()), nil
	}
	profile, err := ProfileForRouting(snapshot)
	if err != nil {
		return nil, err
	}
	full, err := PreviewApply(path, profile)
	if err != nil {
		return nil, err
	}
	policy := policyWithConfigAliases(snapshot)
	if policyLines := rewriteRoutingPolicySections(splitLines(string(full)), policy); len(policyLines) > 0 {
		full = []byte(strings.TrimRight(strings.Join(policyLines, "\n"), "\n") + "\n")
	}
	return full, nil
}

// ApplyOfficialRoutingText removes custom provider definitions while retaining
// the user's selected official Grok model pins. Official model IDs do not need
// [model.*] definitions: the logged-in Grok CLI resolves them from grok.com.
func ApplyOfficialRoutingText(data []byte, policy routing.RoutingPolicy) []byte {
	clean := UseOfficialAuthText(data)
	values := map[string]string{}
	if model := strings.TrimSpace(policy.Default); model != "" {
		values["default"] = quote(model)
	}
	if effort := strings.TrimSpace(policy.DefaultReasoningEffort); effort != "" && effort != "none" {
		values["default_reasoning_effort"] = quote(effort)
	}
	if model := strings.TrimSpace(policy.WebSearch); model != "" {
		values["web_search"] = quote(model)
	}
	if len(values) == 0 && policy.Subagents.Explore == "" && policy.Subagents.Plan == "" {
		return clean
	}
	lines := rewriteOfficialPolicySections(splitLines(string(clean)), values, policy.Subagents)
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n")
}

func rewriteOfficialPolicySections(lines []string, modelValues map[string]string, subagents routing.SubagentsPolicy) []string {
	sectionValues := map[string]map[string]string{}
	if len(modelValues) > 0 {
		sectionValues["models"] = modelValues
	}
	subagentValues := map[string]string{}
	if value := strings.TrimSpace(subagents.Explore); value != "" {
		subagentValues["explore"] = quote(value)
	}
	if value := strings.TrimSpace(subagents.Plan); value != "" {
		subagentValues["plan"] = quote(value)
	}
	if len(subagentValues) > 0 {
		sectionValues["subagents.models"] = subagentValues
	}
	out := make([]string, 0, len(lines)+len(modelValues)+len(subagentValues)+4)
	seen := map[string]bool{}
	for i := 0; i < len(lines); {
		header := parseHeader(lines[i])
		values, managed := sectionValues[header]
		if !managed && header != "subagents.models" {
			out = append(out, lines[i])
			i++
			continue
		}
		end := skipSection(lines, i+1)
		if managed {
			out = append(out, rewriteValues(lines[i:end], values)...)
			seen[header] = true
		}
		i = end
	}
	for _, section := range []string{"models", "subagents.models"} {
		values := sectionValues[section]
		if len(values) == 0 || seen[section] {
			continue
		}
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "["+section+"]")
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out = append(out, key+" = "+values[key])
		}
	}
	return out
}

func SnippetForRouting(snapshot routing.Snapshot) (string, error) {
	profile, err := ProfileForRouting(snapshot)
	if err != nil {
		return "", err
	}
	snippet, err := SnippetForProfile(profile)
	if err != nil {
		return "", err
	}
	policy := snapshot.ActivePolicy()
	var b strings.Builder
	b.WriteString(snippet)
	if policy.WebSearch != "" || policy.Subagents.Explore != "" || policy.Subagents.Plan != "" {
		b.WriteString("\n# 路由策略（模型路由视图管理）：\n")
		if policy.WebSearch != "" {
			b.WriteString("# [models] web_search = " + quote(policy.WebSearch) + "\n")
		}
		b.WriteString("# [subagents.models]\n")
		if policy.Subagents.Explore != "" {
			b.WriteString("#   explore = " + quote(policy.Subagents.Explore) + "\n")
		}
		if policy.Subagents.Plan != "" {
			b.WriteString("#   plan = " + quote(policy.Subagents.Plan) + "\n")
		}
	}
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func CurrentMatchesRouting(path string, snapshot routing.Snapshot) (bool, error) {
	return currentMatchesRouting(path, snapshot, false)
}

// CurrentMatchesRoutingStrictDefaults is used by Collaboration status, where
// the coordinator's default route and reasoning effort are transaction-owned
// and any on-disk drift must be reported. Ordinary routing status continues to
// tolerate Grok persisting a conversation's reasoning effort.
func CurrentMatchesRoutingStrictDefaults(path string, snapshot routing.Snapshot) (bool, error) {
	return currentMatchesRouting(path, snapshot, true)
}

func currentMatchesRouting(path string, snapshot routing.Snapshot, strictDefaults bool) (bool, error) {
	if snapshot.IsOfficial() {
		data, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		official := ApplyOfficialRoutingText(data, snapshot.ActivePolicy())
		return string(data) == string(official), nil
	}
	profile, err := ProfileForRouting(snapshot)
	if err != nil {
		return false, err
	}
	current, err := ImportProfile(path, profile.Name)
	if err != nil {
		return false, err
	}
	// A combined routing profile has no meaningful profile-level API key: every
	// model carries the credential for its own provider. Normalize derives the
	// legacy Profile.APIKey from the first model, but the projected catalog uses
	// provider order while TOML import uses sorted model names. Comparing that
	// synthetic field therefore makes an unchanged multi-provider config appear
	// stale whenever those orders differ. Align only the synthetic aggregate
	// fields; per-model keys, endpoints, headers, backends, and capabilities are
	// still compared strictly below.
	current.APIKey = profile.APIKey
	if !strictDefaults {
		// Grok may persist a conversation's reasoning effort back to config.toml.
		// Ordinary routing status treats it as a runtime preference.
		current.DefaultReasoningEffort = profile.DefaultReasoningEffort
	}
	matches := profiles.Normalize(profile).Matches(profiles.Normalize(current))
	if !matches {
		return false, nil
	}
	// Also verify routing-policy-managed keys match the policy.
	return routingPolicyMatches(path, policyWithConfigAliases(snapshot))
}

// routingPolicyMatches checks whether the on-disk config.toml has the
// routing-policy-managed keys set to the expected values.
func routingPolicyMatches(path string, policy routing.RoutingPolicy) (bool, error) {
	doc, err := readDoc(path)
	if err != nil {
		return false, err
	}
	models := tableAt(doc, "models")
	if stringAt(models, "web_search") != policy.WebSearch {
		return false, nil
	}
	subModels := tableAt(tableAt(doc, "subagents"), "models")
	if stringAt(subModels, "explore") != policy.Subagents.Explore {
		return false, nil
	}
	if stringAt(subModels, "plan") != policy.Subagents.Plan {
		return false, nil
	}
	return true, nil
}

func policyWithConfigAliases(snapshot routing.Snapshot) routing.RoutingPolicy {
	policy := snapshot.ActivePolicy()
	if snapshot.IsOfficial() {
		return policy
	}
	alias := func(ref string) string {
		if route, ok := snapshot.Route(ref); ok {
			return route.Name
		}
		return ref
	}
	policy.Default = alias(policy.Default)
	policy.WebSearch = alias(policy.WebSearch)
	policy.Subagents.Explore = alias(policy.Subagents.Explore)
	policy.Subagents.Plan = alias(policy.Subagents.Plan)
	return policy
}

func profileAnchorAlias(snapshot routing.Snapshot, route routing.ModelRoute) string {
	if route.StandardAnchor == "" {
		return ""
	}
	if anchor, ok := snapshot.Route(route.StandardAnchor); ok {
		return anchor.Name
	}
	return ""
}

func firstNonEmptyRouting(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cloneRoutingHeaders(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
