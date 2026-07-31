package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
)

func ImportProfile(path, name string) (profiles.Profile, error) {
	doc, err := readDoc(path)
	if err != nil {
		return profiles.Profile{}, err
	}
	return profileFromDoc(doc, name)
}

// ValidateProfileEndpointsText checks the provider/model endpoints embedded in
// raw config.toml content before the config editor can persist it.
func ValidateProfileEndpointsText(data []byte) error {
	doc, err := parseDoc(data, "config content")
	if err != nil {
		return err
	}
	_, err = profileFromDoc(doc, "Config")
	return err
}

func profileFromDoc(doc map[string]any, name string) (profiles.Profile, error) {
	profile := profiles.Profile{
		Name:                   name,
		UpstreamFormat:         "openai",
		BaseURL:                stringAt(tableAt(doc, "endpoints"), "models_base_url"),
		DefaultModel:           stringAt(tableAt(doc, "models"), "default"),
		DefaultReasoningEffort: stringAt(tableAt(doc, "models"), "default_reasoning_effort"),
		Models:                 readModels(doc),
	}
	if profile.Name == "" {
		profile.Name = "Default"
	}
	profile = profiles.Normalize(profile)
	if err := profiles.ValidateEndpoints(profile); err != nil {
		return profiles.Profile{}, err
	}
	return profile, nil
}

func ApplyProfileToFile(path string, profile profiles.Profile) error {
	if err := profiles.ValidateEndpoints(profile); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, err := ApplyProfileText(data, profile)
	if err != nil {
		return err
	}
	return atomicWrite(path, next)
}

// UseOfficialAuthToFile removes provider-owned endpoint and model overrides so
// Grok can fall back to the session token managed by `grok login`.
func UseOfficialAuthToFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next := UseOfficialAuthText(data)
	return atomicWrite(path, next)
}

func UseOfficialAuthText(data []byte) []byte {
	data = trimUTF8BOM(data)
	lines := splitLines(string(data))
	var out []string
	for i := 0; i < len(lines); {
		header := parseHeader(lines[i])
		if header == "" {
			out = append(out, lines[i])
			i++
			continue
		}
		if header == "model" || strings.HasPrefix(header, "model.") {
			i = skipSection(lines, i+1)
			continue
		}
		end := skipSection(lines, i+1)
		switch header {
		case "endpoints":
			out = append(out, removeAssignments(lines[i:end], "models_base_url")...)
		case "models":
			out = append(out, removeAssignments(lines[i:end], "default", "web_search", "default_reasoning_effort")...)
		case "subagents":
			// Drop legacy default_model; keep enabled and other user keys.
			out = append(out, removeAssignments(lines[i:end], "default_model")...)
		case "subagents.models":
			// Drop switch-managed type model pins so official auth is clean.
			out = append(out, removeAssignments(lines[i:end], "explore", "plan")...)
		default:
			out = append(out, lines[i:end]...)
		}
		i = end
	}
	result := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if result == "" {
		return []byte{}
	}
	return []byte(result + "\n")
}

func ApplyPrivacyProtectionToFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	next := ApplyPrivacyProtectionText(data)
	return atomicWrite(path, next)
}

func ApplyPrivacyProtectionText(data []byte) []byte {
	settings := map[string]map[string]string{
		"features": {
			"telemetry": "false",
		},
		"telemetry": {
			"trace_upload":     "false",
			"mixpanel_enabled": "false",
		},
		"harness": {
			"disable_codebase_upload": "true",
		},
	}
	data = trimUTF8BOM(data)
	lines := splitLines(string(data))
	var out []string
	seen := make(map[string]bool, len(settings))
	for i := 0; i < len(lines); {
		header := parseHeader(lines[i])
		if header == "" {
			out = append(out, lines[i])
			i++
			continue
		}
		end := skipSection(lines, i+1)
		values, ok := settings[header]
		if ok {
			out = append(out, rewriteValues(lines[i:end], values)...)
			seen[header] = true
		} else {
			out = append(out, lines[i:end]...)
		}
		i = end
	}
	for _, section := range []string{"features", "telemetry", "harness"} {
		if seen[section] {
			continue
		}
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, rewriteValues([]string{"[" + section + "]"}, settings[section])...)
	}
	result := strings.TrimRight(strings.Join(out, "\n"), "\n")
	return []byte(result + "\n")
}

// PreviewApply returns the full config.toml text that would result from
// applying profile onto the existing file (or an empty template if missing).
func PreviewApply(path string, profile profiles.Profile) ([]byte, error) {
	if err := profiles.ValidateEndpoints(profile); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		data = []byte{}
	}
	return ApplyProfileText(data, profile)
}

// SnippetForProfile returns only the provider-owned sections as a readable TOML fragment.
func SnippetForProfile(profile profiles.Profile) (string, error) {
	profile = profiles.Normalize(profile)
	if err := profiles.ValidateEndpoints(profile); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# 此供应商启用时会写入/覆盖的片段（其它段落保留）\n\n")
	b.WriteString("[endpoints]\n")
	b.WriteString("models_base_url = " + quote(profile.BaseURL) + "\n\n")
	b.WriteString("[models]\n")
	b.WriteString("default = " + quote(profile.DefaultModel) + "\n")
	if effort := strings.TrimSpace(profile.DefaultReasoningEffort); effort != "" && effort != "none" {
		b.WriteString("default_reasoning_effort = " + quote(effort) + "\n")
	}
	modelData, err := marshalModelSection(profile)
	if err != nil {
		return "", err
	}
	b.Write(modelData)
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func ApplyProfile(doc map[string]any, profile profiles.Profile) {
	profile = profiles.Normalize(profile)
	endpoints := ensureTable(doc, "endpoints")
	endpoints["models_base_url"] = profile.BaseURL

	models := ensureTable(doc, "models")
	models["default"] = profile.DefaultModel
	if effort := strings.TrimSpace(profile.DefaultReasoningEffort); effort != "" && effort != "none" {
		models["default_reasoning_effort"] = effort
	} else {
		delete(models, "default_reasoning_effort")
	}

	modelTable := make(map[string]any, len(profile.Models))
	effectiveKey := profile.EffectiveAPIKey()
	for _, model := range profile.Models {
		key := model.Name
		if key == "" {
			key = model.Model
		}
		apiKey := model.APIKey
		if apiKey == "" {
			apiKey = effectiveKey
		}
		entry := map[string]any{
			"model":                     model.Model,
			"api_key":                   apiKey,
			"api_backend":               model.APIBackend,
			"supports_backend_search":   model.SupportsBackendSearch,
			"supports_reasoning_effort": model.SupportsReasoningEffort,
			"reasoning_efforts":         model.ReasoningEfforts,
		}
		if strings.TrimSpace(model.ReasoningEffortsSource) != "" {
			entry["reasoning_efforts_source"] = model.ReasoningEffortsSource
		}
		// Omit zero values so Grok uses its own defaults:
		// - omitted context_window → ~200k for new models (or built-in inherit)
		// - omitted max_completion_tokens → global [models] default if set
		if model.ContextWindow > 0 {
			entry["context_window"] = model.ContextWindow
		}
		if model.MaxCompletionTokens > 0 {
			entry["max_completion_tokens"] = model.MaxCompletionTokens
		}
		if model.BaseURL != "" {
			entry["base_url"] = model.BaseURL
		}
		if len(model.ExtraHeaders) > 0 {
			entry["extra_headers"] = model.ExtraHeaders
		}
		modelTable[key] = entry
	}
	doc["model"] = modelTable
}

func ApplyProfileText(data []byte, profile profiles.Profile) ([]byte, error) {
	data = trimUTF8BOM(data)
	profile = profiles.Normalize(profile)
	if err := profiles.ValidateEndpoints(profile); err != nil {
		return nil, err
	}
	newModelData, err := marshalModelSection(profile)
	if err != nil {
		return nil, err
	}
	lines := splitLines(string(data))
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(lines); {
		header := parseHeader(lines[i])
		if header == "" {
			out = append(out, lines[i])
			i++
			continue
		}
		if header == "model" || strings.HasPrefix(header, "model.") {
			i = skipSection(lines, i+1)
			continue
		}
		// subagents.models is managed exclusively by routing policy, not by
		// profile activation. Skip it here so routing policy stays authoritative.
		if header == "subagents.models" {
			i = skipSection(lines, i+1)
			continue
		}
		if header == "subagents" {
			// Preserve [subagents] keys (e.g. enabled) but drop legacy default_model.
			end := skipSection(lines, i+1)
			out = append(out, removeAssignments(lines[i:end], "default_model")...)
			seen["subagents"] = true
			i = end
			continue
		}
		if header == "endpoints" || header == "models" {
			end := skipSection(lines, i+1)
			out = append(out, rewriteSection(lines[i:end], header, profile)...)
			seen[header] = true
			i = end
			continue
		}
		end := skipSection(lines, i+1)
		out = append(out, lines[i:end]...)
		i = end
	}
	for _, section := range []string{"endpoints", "models"} {
		if !seen[section] {
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, "")
			}
			out = append(out, rewriteSection([]string{"[" + section + "]"}, section, profile)...)
		}
	}
	if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
		out = append(out, "")
	}
	out = append(out, strings.TrimRight(string(newModelData), "\r\n"))
	result := strings.Join(out, "\n")
	if !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return []byte(result), nil
}

func CurrentMatches(path string, profile profiles.Profile) (bool, error) {
	current, err := ImportProfile(path, profile.Name)
	if err != nil {
		return false, err
	}
	// Grok may persist the reasoning effort selected in a conversation back to
	// config.toml. That is a runtime preference, not a provider-routing change,
	// so it must not make the UI permanently claim that the active supplier is
	// mismatched. Keep strict comparison for endpoints, models, API keys,
	// backends, search/subagent routing, and model capabilities.
	current.DefaultReasoningEffort = profile.DefaultReasoningEffort
	// Compare normalized views: ApplyProfile fills per-model base_url/api_key
	// into config.toml, while stored profiles may keep those fields empty.
	return profiles.Normalize(profile).Matches(profiles.Normalize(current)), nil
}

func readDoc(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseDoc(data, path)
}

func parseDoc(data []byte, source string) (map[string]any, error) {
	data = trimUTF8BOM(data)
	doc := map[string]any{}
	if strings.TrimSpace(string(data)) == "" {
		return doc, nil
	}
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	return doc, nil
}

func readModels(doc map[string]any) []profiles.ModelDef {
	modelTable := tableAt(doc, "model")
	keys := make([]string, 0, len(modelTable))
	for key := range modelTable {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]profiles.ModelDef, 0, len(keys))
	for _, key := range keys {
		table, ok := modelTable[key].(map[string]any)
		if !ok {
			continue
		}
		out = append(out, profiles.ModelDef{
			Name:                    key,
			Model:                   stringAt(table, "model"),
			BaseURL:                 stringAt(table, "base_url"),
			APIKey:                  stringAt(table, "api_key"),
			APIBackend:              stringAt(table, "api_backend"),
			ExtraHeaders:            stringMapAt(table, "extra_headers"),
			SupportsBackendSearch:   boolAt(table, "supports_backend_search"),
			SupportsReasoningEffort: boolAt(table, "supports_reasoning_effort"),
			ReasoningEfforts:        stringSliceAt(table, "reasoning_efforts"),
			ReasoningEffortsSource:  stringAt(table, "reasoning_efforts_source"),
			ContextWindow:           intAt(table, "context_window"),
			MaxCompletionTokens:     intAt(table, "max_completion_tokens"),
		})
	}
	return out
}

func marshalModelSection(profile profiles.Profile) ([]byte, error) {
	doc := map[string]any{}
	ApplyProfile(doc, profile)
	delete(doc, "endpoints")
	delete(doc, "models")
	delete(doc, "subagents")
	return toml.Marshal(doc)
}

// applyRoutingPolicyToDoc writes routing-policy-managed TOML keys into a
// document map. These keys are owned by the routing layer, not by individual
// profiles: [models].web_search and [subagents.models].explore/plan.
func applyRoutingPolicyToDoc(doc map[string]any, policy routing.RoutingPolicy) {
	if policy.WebSearch != "" {
		ensureTable(doc, "models")["web_search"] = policy.WebSearch
	}
	sub := ensureTable(doc, "subagents")
	delete(sub, "default_model")
	models := map[string]any{}
	if strings.TrimSpace(policy.Subagents.Explore) != "" {
		models["explore"] = policy.Subagents.Explore
	}
	if strings.TrimSpace(policy.Subagents.Plan) != "" {
		models["plan"] = policy.Subagents.Plan
	}
	if len(models) > 0 {
		sub["models"] = models
	} else {
		delete(sub, "models")
	}
	if len(sub) == 0 {
		delete(doc, "subagents")
	}
}

// rewriteRoutingPolicySections rewrites the routing-policy-managed TOML
// sections ([models].web_search and [subagents.models].*) from a routing policy.
func rewriteRoutingPolicySections(lines []string, policy routing.RoutingPolicy) []string {
	values := map[string]string{}
	webSearch := strings.TrimSpace(policy.WebSearch)
	if webSearch != "" {
		values["web_search"] = quote(webSearch)
	}
	if v := strings.TrimSpace(policy.Subagents.Explore); v != "" {
		values["explore"] = quote(v)
	}
	if v := strings.TrimSpace(policy.Subagents.Plan); v != "" {
		values["plan"] = quote(v)
	}
	// Both sections are always managed so clearing optional policy fields also
	// removes stale TOML assignments instead of leaving the old route active.
	sectionManaged := map[string]bool{"models": true, "subagents.models": true}
	sectionValues := map[string]map[string]string{}
	for key, val := range values {
		section := "models"
		if key != "web_search" {
			section = "subagents.models"
		}
		if sectionValues[section] == nil {
			sectionValues[section] = map[string]string{}
		}
		sectionValues[section][key] = val
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(lines)+len(values))
	for i := 0; i < len(lines); {
		header := parseHeader(lines[i])
		if header == "" || !sectionManaged[header] {
			out = append(out, lines[i])
			i++
			continue
		}
		end := skipSection(lines, i+1)
		sv := sectionValues[header]
		if sv != nil {
			out = append(out, rewriteValues(lines[i:end], sv)...)
			for key := range sv {
				seen[key] = true
			}
		} else if header == "models" {
			out = append(out, removeAssignments(lines[i:end], "web_search")...)
		}
		// An empty subagents.models policy removes the managed section entirely.
		i = end
	}
	for _, section := range []string{"models", "subagents.models"} {
		sv := sectionValues[section]
		if sv == nil {
			continue
		}
		missing := make([]string, 0, len(sv))
		for key := range sv {
			if !seen[key] {
				missing = append(missing, key)
			}
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "["+section+"]")
		for _, key := range missing {
			out = append(out, key+" = "+sv[key])
		}
	}
	return out
}

func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func parseHeader(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return ""
	}
	trimmed = strings.Trim(trimmed, "[]")
	return strings.Trim(trimmed, " ")
}

func skipSection(lines []string, start int) int {
	for start < len(lines) {
		if parseHeader(lines[start]) != "" {
			return start
		}
		start++
	}
	return start
}

func rewriteSection(lines []string, section string, profile profiles.Profile) []string {
	values := map[string]string{}
	switch section {
	case "endpoints":
		values["models_base_url"] = quote(profile.BaseURL)
	case "models":
		values["default"] = quote(profile.DefaultModel)
		values["default_reasoning_effort"] = quote(profile.DefaultReasoningEffort)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(lines)+len(values))
	if len(lines) == 0 {
		out = append(out, "["+section+"]")
	} else {
		out = append(out, lines[0])
	}
	for _, line := range lines[1:] {
		key := assignmentKey(line)
		if _, ok := values[key]; ok {
			out = append(out, key+" = "+values[key])
			seen[key] = true
			continue
		}
		out = append(out, line)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+" = "+values[key])
	}
	return out
}

func assignmentKey(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	idx := strings.Index(trimmed, "=")
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(trimmed[:idx])
}

func removeAssignments(lines []string, keys ...string) []string {
	removed := make(map[string]bool, len(keys))
	for _, key := range keys {
		removed[key] = true
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if !removed[assignmentKey(line)] {
			out = append(out, line)
		}
	}
	return out
}

func rewriteValues(lines []string, values map[string]string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(lines)+len(values))
	if len(lines) == 0 {
		return out
	}
	out = append(out, lines[0])
	for _, line := range lines[1:] {
		key := assignmentKey(line)
		value, ok := values[key]
		if ok {
			out = append(out, key+" = "+value)
			seen[key] = true
			continue
		}
		out = append(out, line)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+" = "+values[key])
	}
	return out
}

func quote(value string) string {
	var buf bytes.Buffer
	encoder := toml.NewEncoder(&buf)
	_ = encoder.Encode(map[string]string{"x": value})
	line := strings.TrimSpace(buf.String())
	return strings.TrimSpace(strings.TrimPrefix(line, "x = "))
}

func ensureTable(doc map[string]any, key string) map[string]any {
	if table, ok := doc[key].(map[string]any); ok {
		return table
	}
	table := map[string]any{}
	doc[key] = table
	return table
}

func tableAt(doc map[string]any, key string) map[string]any {
	if table, ok := doc[key].(map[string]any); ok {
		return table
	}
	return map[string]any{}
}

func stringAt(table map[string]any, key string) string {
	if v, ok := table[key].(string); ok {
		return v
	}
	return ""
}

func intAt(table map[string]any, key string) int64 {
	switch v := table[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func boolAt(table map[string]any, key string) bool {
	switch v := table[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true") || v == "1"
	case int64:
		return v != 0
	case int:
		return v != 0
	default:
		return false
	}
}

func stringMapAt(table map[string]any, key string) map[string]string {
	raw, ok := table[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch s := v.(type) {
		case string:
			out[k] = s
		case bool:
			if s {
				out[k] = "true"
			} else {
				out[k] = "false"
			}
		case int64:
			out[k] = fmt.Sprintf("%d", s)
		case int:
			out[k] = fmt.Sprintf("%d", s)
		case float64:
			out[k] = fmt.Sprintf("%v", s)
		default:
			out[k] = fmt.Sprintf("%v", s)
		}
	}
	return out
}

func stringSliceAt(table map[string]any, key string) []string {
	raw, ok := table[key].([]any)
	if !ok {
		if values, stringsOK := table[key].([]string); stringsOK {
			return append([]string(nil), values...)
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, textOK := value.(string); textOK && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

func trimUTF8BOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		if runtime.GOOS == "windows" {
			if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
				return err
			}
			return os.Rename(tmpName, path)
		}
		return err
	}
	return nil
}
