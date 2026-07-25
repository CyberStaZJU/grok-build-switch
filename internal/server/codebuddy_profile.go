package server

import (
	"context"
	"fmt"

	"grok_switch/internal/profiles"
)

const (
	codeBuddyProfileSource = "codebuddy:local"
	codeBuddyProfileName   = "CodeBuddy 内置模型"
	codeBuddyLocalAPIKey   = "local-codebuddy"
)

// EnsureCodeBuddyProfile exposes the locally installed CodeBuddy models as one
// managed provider profile. It only uses Inspect's public CLI metadata and does
// not activate the profile or otherwise change the current provider selection.
func (s *Server) EnsureCodeBuddyProfile() error {
	if s.Profiles == nil || s.ActualPort == 0 {
		return nil
	}
	status := s.codeBuddyRunner().Inspect(context.Background())
	if !status.Available {
		// Keep an existing managed profile untouched during transient discovery
		// failures so startup and the user's current routing remain stable.
		return nil
	}
	models := externalCodeBuddyModels(status.Models)
	if len(models) == 0 {
		return nil
	}

	list, err := s.Profiles.List()
	if err != nil {
		return err
	}
	managed := make([]profiles.Profile, 0, 1)
	for _, profile := range list {
		if profile.Source == codeBuddyProfileSource {
			managed = append(managed, profile)
		}
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d/codebuddy/v1", s.ActualPort)
	desired := profiles.Profile{
		Name:            codeBuddyProfileName,
		Source:          codeBuddyProfileSource,
		UpstreamFormat:  "openai_chat",
		BaseURL:         baseURL,
		APIKey:          codeBuddyLocalAPIKey,
		AvailableModels: append([]string(nil), models...),
		DefaultModel:    models[0],
		Models:          make([]profiles.ModelDef, 0, len(models)),
	}
	for _, model := range models {
		desired.Models = append(desired.Models, profiles.ModelDef{
			Name:       model,
			Model:      model,
			BaseURL:    baseURL,
			APIKey:     codeBuddyLocalAPIKey,
			APIBackend: "chat_completions",
		})
	}

	if len(managed) == 0 {
		_, err = s.Profiles.Create(desired)
		return err
	}
	canonical := managed[0]
	if _, err = s.Profiles.Update(canonical.ID, desired); err != nil {
		return err
	}
	for _, profile := range managed {
		if profile.ID != canonical.ID {
			if err := s.Profiles.Delete(profile.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
