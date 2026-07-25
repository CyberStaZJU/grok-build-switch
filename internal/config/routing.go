package config

import (
	"fmt"
	"os"
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
	profile := profiles.Profile{
		Name:                   "Routing",
		DefaultModel:           snapshot.Policy.Default,
		DefaultReasoningEffort: snapshot.Policy.DefaultReasoningEffort,
		Models:                 make([]profiles.ModelDef, 0, len(snapshot.ModelRoutes)),
	}
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
			ContextWindow:           route.ContextWindow,
			MaxCompletionTokens:     route.MaxCompletionTokens,
		})
	}
	// Per-model base_url is authoritative for a multi-provider config. Keep the
	// legacy global endpoint aligned with the default route for compatibility.
	if route, ok := snapshot.Route(snapshot.Policy.Default); ok {
		provider, _ := snapshot.Provider(route.ProviderID)
		profile.BaseURL = firstNonEmptyRouting(route.BaseURL, provider.BaseURL)
	}
	return profiles.Normalize(profile), nil
}

func ApplyRoutingToFile(path string, snapshot routing.Snapshot) error {
	if snapshot.Policy.Official {
		return UseOfficialAuthToFile(path)
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
	if policyLines := rewriteRoutingPolicySections(splitLines(string(next)), snapshot.Policy); len(policyLines) > 0 {
		next = []byte(strings.TrimRight(strings.Join(policyLines, "\n"), "\n") + "\n")
	}
	return atomicWrite(path, next)
}

func PreviewRouting(path string, snapshot routing.Snapshot) ([]byte, error) {
	if snapshot.Policy.Official {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return UseOfficialAuthText(data), nil
	}
	profile, err := ProfileForRouting(snapshot)
	if err != nil {
		return nil, err
	}
	full, err := PreviewApply(path, profile)
	if err != nil {
		return nil, err
	}
	if policyLines := rewriteRoutingPolicySections(splitLines(string(full)), snapshot.Policy); len(policyLines) > 0 {
		full = []byte(strings.TrimRight(strings.Join(policyLines, "\n"), "\n") + "\n")
	}
	return full, nil
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
	policy := snapshot.Policy
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
	if snapshot.Policy.Official {
		data, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		official := UseOfficialAuthText(data)
		return string(data) == string(official), nil
	}
	profile, err := ProfileForRouting(snapshot)
	if err != nil {
		return false, err
	}
	matches, err := CurrentMatches(path, profile)
	if err != nil || !matches {
		return matches, err
	}
	// Also verify routing-policy-managed keys match the policy.
	return routingPolicyMatches(path, snapshot.Policy)
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
