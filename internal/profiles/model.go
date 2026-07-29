package profiles

import (
	"fmt"
	"strings"
	"time"
)

type ModelDef struct {
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

type Profile struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Source                 string     `json:"source,omitempty"`
	Template               string     `json:"template,omitempty"`
	UpstreamFormat         string     `json:"upstream_format"`
	BaseURL                string     `json:"base_url"`
	APIKey                 string     `json:"api_key"`
	AvailableModels        []string   `json:"available_models"`
	DefaultModel           string     `json:"default_model"`
	DefaultReasoningEffort string     `json:"default_reasoning_effort"`
	Models                 []ModelDef `json:"models"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

func (p Profile) Matches(other Profile) bool {
	p = Normalize(p)
	other = Normalize(other)
	if p.BaseURL != other.BaseURL ||
		p.DefaultModel != other.DefaultModel ||
		p.DefaultReasoningEffort != other.DefaultReasoningEffort {
		return false
	}
	// config.toml only stores keys on [model.*] entries. A profile with no
	// enabled models cannot persist its profile-level api_key, so skip key
	// comparison when both sides have zero model definitions.
	if len(p.Models) == 0 && len(other.Models) == 0 {
		return true
	}
	if effectiveAPIKey(p) != effectiveAPIKey(other) || len(p.Models) != len(other.Models) {
		return false
	}
	byName := make(map[string]ModelDef, len(p.Models))
	for _, model := range p.Models {
		byName[modelKey(model)] = model
	}
	for _, model := range other.Models {
		stored, ok := byName[modelKey(model)]
		if !ok || !modelEqual(stored, model) {
			return false
		}
	}
	return true
}

func modelKey(m ModelDef) string {
	if m.Name != "" {
		return m.Name
	}
	return m.Model
}

func modelEqual(a, b ModelDef) bool {
	if modelKey(a) != modelKey(b) ||
		a.Model != b.Model ||
		a.BaseURL != b.BaseURL ||
		a.APIKey != b.APIKey ||
		a.APIBackend != b.APIBackend ||
		a.SupportsBackendSearch != b.SupportsBackendSearch ||
		a.SupportsReasoningEffort != b.SupportsReasoningEffort ||
		a.ReasoningEffortsSource != b.ReasoningEffortsSource ||
		a.ContextWindow != b.ContextWindow ||
		a.MaxCompletionTokens != b.MaxCompletionTokens {
		return false
	}
	if !stringSlicesEqual(a.ReasoningEfforts, b.ReasoningEfforts) {
		return false
	}
	if len(a.ExtraHeaders) != len(b.ExtraHeaders) {
		return false
	}
	for k, v := range a.ExtraHeaders {
		if b.ExtraHeaders[k] != v {
			return false
		}
	}
	return true
}

func (p Profile) EffectiveAPIKey() string {
	return effectiveAPIKey(p)
}

// ValidateDefaultReasoningEffort ensures the profile-level default is accepted
// by the selected default model. Grok Build treats each model's advertised
// reasoning menu as authoritative rather than accepting every canonical tier.
func ValidateDefaultReasoningEffort(p Profile) error {
	p = Normalize(p)
	effort := strings.TrimSpace(p.DefaultReasoningEffort)
	if effort == "" || strings.TrimSpace(p.DefaultModel) == "" {
		return nil
	}
	for _, model := range p.Models {
		if model.Name != p.DefaultModel && model.Model != p.DefaultModel {
			continue
		}
		if containsString(model.ReasoningEfforts, effort) {
			return nil
		}
		return fmt.Errorf("模型 %q 不支持推理强度 %q；可用档位：%s", p.DefaultModel, effort, strings.Join(model.ReasoningEfforts, "、"))
	}
	return fmt.Errorf("默认模型 %q 不在已启用模型列表中", p.DefaultModel)
}

func Normalize(p Profile) Profile {
	if p.DefaultReasoningEffort == "" {
		p.DefaultReasoningEffort = "high"
	}
	if p.UpstreamFormat == "" {
		p.UpstreamFormat = "openai_chat"
	}
	if p.UpstreamFormat == "openai" || p.UpstreamFormat == "grok" {
		p.UpstreamFormat = "openai_chat"
	}
	if p.APIKey == "" {
		p.APIKey = effectiveAPIKey(p)
	}
	// Profiles with only default model names (no models[]) still need a
	// writable [model.*] entry so config.toml can store the API key.
	if len(p.Models) == 0 {
		names := uniqueStrings([]string{
			p.DefaultModel,
		})
		for _, name := range names {
			if name == "" {
				continue
			}
			p.Models = append(p.Models, ModelDef{
				Name:  name,
				Model: name,
			})
		}
	}
	for i := range p.Models {
		if p.Models[i].Name == "" {
			p.Models[i].Name = p.Models[i].Model
		}
		if p.Models[i].Model == "" {
			p.Models[i].Model = p.Models[i].Name
		}
		if p.Models[i].BaseURL == "" {
			p.Models[i].BaseURL = p.BaseURL
		}
		if p.Models[i].APIKey == "" {
			p.Models[i].APIKey = p.APIKey
		}
		if p.Models[i].APIBackend == "" {
			p.Models[i].APIBackend = APIBackendForUpstreamFormat(p.UpstreamFormat)
		}
		if p.Models[i].ExtraHeaders == nil {
			p.Models[i].ExtraHeaders = map[string]string{}
		}
		p.Models[i].SupportsReasoningEffort = true
		if len(p.Models[i].ReasoningEfforts) == 0 {
			p.Models[i].ReasoningEfforts = []string{"low", "medium", "high"}
		} else {
			p.Models[i].ReasoningEfforts = uniqueStrings(p.Models[i].ReasoningEfforts)
		}
		if p.Models[i].ReasoningEffortsSource == "" {
			p.Models[i].ReasoningEffortsSource = "default"
		}
	}
	p.AvailableModels = uniqueStrings(p.AvailableModels)
	return p
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func APIBackendForUpstreamFormat(upstreamFormat string) string {
	switch upstreamFormat {
	case "openai_responses", "responses":
		return "responses"
	case "anthropic", "messages":
		return "messages"
	case "openai_chat", "openai", "grok", "custom", "chat_completions":
		return "chat_completions"
	default:
		return "chat_completions"
	}
}

func effectiveAPIKey(p Profile) string {
	if p.APIKey != "" {
		return p.APIKey
	}
	for _, model := range p.Models {
		if model.APIKey != "" {
			return model.APIKey
		}
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
