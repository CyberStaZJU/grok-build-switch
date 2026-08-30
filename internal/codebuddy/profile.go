package codebuddy

import (
	"strings"

	"grok_switch/internal/profiles"
)

// ProfileName is the user-visible supplier name in Switch.
const ProfileName = "CodeBuddy / WorkBuddy"

// IsKnownModel reports whether id is in the Switch fallback catalog.
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
// CodeBuddy proxy using the Switch fallback catalog.
func NewProfile(baseURL, apiKey string) profiles.Profile {
	return NewProfileFromCatalog(baseURL, apiKey, BuiltInCatalog())
}

// NewProfileFromCatalog builds a managed profile from the latest available
// WorkBuddy CLI model directory.
func NewProfileFromCatalog(baseURL, apiKey string, catalog Catalog) profiles.Profile {
	models := make([]profiles.ModelDef, 0, len(catalog.Models))
	available := make([]string, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		if !IsValidModelID(model.ID) || !model.SupportsToolCall {
			continue
		}
		models = append(models, ModelDefinition(model, baseURL, apiKey))
		available = append(available, model.ID)
	}
	if len(models) == 0 {
		fallback := BuiltInCatalog()
		if catalog.Source != fallback.Source {
			return NewProfileFromCatalog(baseURL, apiKey, fallback)
		}
	}
	defaultModel := DefaultModel
	if !containsModelID(available, defaultModel) && len(available) > 0 {
		defaultModel = available[0]
	}
	return profiles.Profile{
		Name:            ProfileName,
		Source:          SourceTag,
		UpstreamFormat:  "openai_chat",
		BaseURL:         baseURL,
		APIKey:          apiKey,
		AvailableModels: available,
		DefaultModel:    defaultModel,
		Models:          models,
	}
}

func containsModelID(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
