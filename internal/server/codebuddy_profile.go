package server

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
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
	legacy := make([]profiles.Profile, 0, 1)
	for _, profile := range list {
		if profile.Source == codeBuddyProfileSource {
			managed = append(managed, profile)
			continue
		}
		if isLegacyCodeBuddyProfile(profile) {
			legacy = append(legacy, profile)
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

	var canonical profiles.Profile
	if len(managed) == 0 {
		canonical, err = s.Profiles.Create(desired)
		if err != nil {
			return err
		}
	} else {
		canonical = managed[0]
		if _, err = s.Profiles.Update(canonical.ID, desired); err != nil {
			return err
		}
	}

	duplicates := append([]profiles.Profile(nil), legacy...)
	for _, profile := range managed {
		if profile.ID != canonical.ID {
			duplicates = append(duplicates, profile)
		}
	}
	if len(duplicates) == 0 {
		return nil
	}

	// Preserve selections that pointed at a duplicate before deleting it. The
	// replacement catalog already excludes duplicates, so a later read can never
	// observe stale policy references even if profile cleanup is interrupted.
	if err := s.rewriteCodeBuddyDuplicatePolicy(list, canonical.ID, duplicates); err != nil {
		return err
	}
	for _, profile := range duplicates {
		if err := s.Profiles.Delete(profile.ID); err != nil {
			return err
		}
	}
	return nil
}

func isLegacyCodeBuddyProfile(profile profiles.Profile) bool {
	if profile.Source != "" || profile.Name != codeBuddyProfileName || profile.APIKey != codeBuddyLocalAPIKey || profile.UpstreamFormat != "openai_chat" {
		return false
	}
	if !isLoopbackCodeBuddyURL(profile.BaseURL) || len(profile.Models) == 0 {
		return false
	}
	for _, model := range profile.Models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = strings.TrimSpace(model.Model)
		}
		if !strings.HasPrefix(name, codeBuddyModelPrefix) || model.Model != name || model.APIKey != codeBuddyLocalAPIKey || model.APIBackend != "chat_completions" || !isLoopbackCodeBuddyURL(model.BaseURL) {
			return false
		}
	}
	return true
}

func isLoopbackCodeBuddyURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "http" || parsed.Path != "/codebuddy/v1" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	return host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func (s *Server) rewriteCodeBuddyDuplicatePolicy(before []profiles.Profile, canonicalID string, duplicates []profiles.Profile) error {
	if s.Routing == nil {
		return nil
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		return err
	}
	beforeCatalog := routing.Project(before)
	duplicateIDs := make(map[string]bool, len(duplicates))
	for _, profile := range duplicates {
		duplicateIDs[profile.ID] = true
	}
	afterProfiles, err := s.Profiles.List()
	if err != nil {
		return err
	}
	remaining := afterProfiles[:0]
	for _, profile := range afterProfiles {
		if !duplicateIDs[profile.ID] {
			remaining = append(remaining, profile)
		}
	}
	afterCatalog := routing.Project(remaining)
	translate := func(selected string) string {
		if selected == "" {
			return ""
		}
		route, ok := beforeCatalog.Route(selected)
		if !ok || !duplicateIDs[route.ProviderID] {
			return selected
		}
		for _, candidate := range afterCatalog.ModelRoutes {
			if candidate.ProviderID == canonicalID && candidate.ProfileModel == route.ProfileModel {
				return candidate.Name
			}
		}
		return selected
	}
	policy := stored.Policy
	policy.Default = translate(policy.Default)
	policy.WebSearch = translate(policy.WebSearch)
	policy.Subagents.Explore = translate(policy.Subagents.Explore)
	policy.Subagents.Plan = translate(policy.Subagents.Plan)
	// Replace the catalog and policy together. Updating only the policy would be
	// rejected because the old persisted catalog does not yet contain the newly
	// unqualified canonical route names.
	afterCatalog.Policy = policy
	_, err = s.Routing.Replace(afterCatalog)
	return err
}
