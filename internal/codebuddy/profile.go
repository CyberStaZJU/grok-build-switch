package codebuddy

import (
	"strings"

	"grok_switch/internal/profiles"
)

// ProfileName is the user-visible supplier name in Switch.
const ProfileName = "CodeBuddy / WorkBuddy"

// IsKnownModel reports whether id is in the managed catalog.
func IsKnownModel(id string) bool {
	id = strings.TrimSpace(id)
	for _, known := range KnownModels {
		if known == id {
			return true
		}
	}
	return false
}

// NewProfile builds a managed profile that routes through the in-process
// CodeBuddy proxy. baseURL must be the Switch-local OpenAI root, e.g.
// http://127.0.0.1:17878/codebuddy-proxy/v1
func NewProfile(baseURL, apiKey string) profiles.Profile {
	models := make([]profiles.ModelDef, 0, len(KnownModels))
	for _, id := range KnownModels {
		models = append(models, profiles.ModelDef{
			Name:                  id,
			Model:                 id,
			BaseURL:               baseURL,
			APIKey:                apiKey,
			APIBackend:            "chat_completions",
			SupportsBackendSearch: false,
			// Unknown reasoning menus: leave unset so Grok does not force tiers.
			SupportsReasoningEffort: false,
			ContextWindow:           128000,
			MaxCompletionTokens:     8192,
			StreamToolCalls:         profiles.BoolPtr(false),
		})
	}
	return profiles.Profile{
		Name:            ProfileName,
		Source:          SourceTag,
		UpstreamFormat:  "openai_chat",
		BaseURL:         baseURL,
		APIKey:          apiKey,
		AvailableModels: append([]string(nil), KnownModels...),
		DefaultModel:    DefaultModel,
		Models:          models,
	}
}
