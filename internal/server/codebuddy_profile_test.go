package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/codebuddy"
	grokconfig "grok_switch/internal/config"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
)

func TestEnsureCodeBuddyProfileCreatesSafeDeduplicatedManagedProfile(t *testing.T) {
	store := profiles.NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	if _, err := store.Create(profiles.Profile{Name: "Current", BaseURL: "https://example.test/v1", APIKey: "secret", DefaultModel: "current"}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeCodeBuddyRunner{status: codebuddy.Status{
		Available: true,
		Path:      "/Users/private/.local/bin/codebuddy",
		Error:     "token at /Users/private/.codebuddy/auth.json",
		Models:    []string{"hy3", "codebuddy/glm-5.2", "hy3", "--unsafe"},
	}}
	s := &Server{Profiles: store, CodeBuddy: fake, ActualPort: 18787}
	if err := s.EnsureCodeBuddyProfile(); err != nil {
		t.Fatal(err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var managed []profiles.Profile
	for _, profile := range list {
		if profile.Source == codeBuddyProfileSource {
			managed = append(managed, profile)
		}
	}
	if len(managed) != 1 {
		t.Fatalf("managed profiles = %d, want 1: %#v", len(managed), list)
	}
	profile := managed[0]
	if profile.Name != codeBuddyProfileName || profile.BaseURL != "http://127.0.0.1:18787/codebuddy/v1" || profile.UpstreamFormat != "openai_chat" || profile.APIKey != codeBuddyLocalAPIKey {
		t.Fatalf("managed profile = %#v", profile)
	}
	if profile.DefaultModel != "codebuddy/hy3" || len(profile.Models) != 2 {
		t.Fatalf("models/default = %#v / %q", profile.Models, profile.DefaultModel)
	}
	for _, model := range profile.Models {
		if !strings.HasPrefix(model.Name, codeBuddyModelPrefix) || strings.HasPrefix(model.Name, codeBuddyModelPrefix+codeBuddyModelPrefix) || model.Model != model.Name || model.APIBackend != "chat_completions" || model.BaseURL != profile.BaseURL || model.APIKey != codeBuddyLocalAPIKey {
			t.Fatalf("unsafe managed model = %#v", model)
		}
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/Users/private", "auth.json", "token at"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("profiles leaked %q: %s", forbidden, raw)
		}
	}
}

func TestEnsureCodeBuddyProfileUnavailableDoesNotCreateOrDestroy(t *testing.T) {
	store := profiles.NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	existing, err := store.Create(profiles.Profile{
		Name: codeBuddyProfileName, Source: codeBuddyProfileSource,
		BaseURL: "http://127.0.0.1:1111/codebuddy/v1", APIKey: codeBuddyLocalAPIKey,
		DefaultModel: "codebuddy/hy3", Models: []profiles.ModelDef{{Name: "codebuddy/hy3", Model: "codebuddy/hy3"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Profiles: store, CodeBuddy: &fakeCodeBuddyRunner{status: codebuddy.Status{Error: "private path /secret"}}, ActualPort: 2222}
	if err := s.EnsureCodeBuddyProfile(); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != existing.BaseURL || got.DefaultModel != existing.DefaultModel {
		t.Fatalf("unavailable inspect changed managed profile: %#v", got)
	}

	empty := profiles.NewStore(filepath.Join(t.TempDir(), "empty-profiles.json"))
	s.Profiles = empty
	if err := s.EnsureCodeBuddyProfile(); err != nil {
		t.Fatal(err)
	}
	list, err := empty.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("unavailable inspect created profile: %#v, err=%v", list, err)
	}
}

func TestEnsureCodeBuddyProfileUpdatesPortAndRemovesDuplicates(t *testing.T) {
	store := profiles.NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	first, err := store.Create(profiles.Profile{Name: "old one", Source: codeBuddyProfileSource, BaseURL: "http://127.0.0.1:1001/codebuddy/v1", DefaultModel: "codebuddy/old"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(profiles.Profile{Name: "old two", Source: codeBuddyProfileSource, BaseURL: "http://127.0.0.1:1002/codebuddy/v1", DefaultModel: "codebuddy/old"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Profiles: store, CodeBuddy: &fakeCodeBuddyRunner{status: codebuddy.Status{Available: true, Models: []string{"hy3"}}}, ActualPort: 29999}
	if err := s.EnsureCodeBuddyProfile(); err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var managed []profiles.Profile
	for _, profile := range list {
		if profile.Source == codeBuddyProfileSource {
			managed = append(managed, profile)
		}
	}
	if len(managed) != 1 || managed[0].ID != first.ID {
		t.Fatalf("managed profiles after dedupe = %#v (first=%s second=%s)", managed, first.ID, second.ID)
	}
	if managed[0].BaseURL != "http://127.0.0.1:29999/codebuddy/v1" || managed[0].Models[0].BaseURL != managed[0].BaseURL {
		t.Fatalf("port was not updated: %#v", managed[0])
	}
}

func TestEnsureCodeBuddyProfileMigratesLegacyDuplicateAndRewritesPolicy(t *testing.T) {
	dir := t.TempDir()
	store := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	legacy, err := store.Create(profiles.Profile{
		Name: codeBuddyProfileName, UpstreamFormat: "openai_chat",
		BaseURL: "http://127.0.0.1:17878/codebuddy/v1", APIKey: codeBuddyLocalAPIKey,
		DefaultModel: "codebuddy/hy3", Models: []profiles.ModelDef{
			{Name: "codebuddy/hy3", Model: "codebuddy/hy3", BaseURL: "http://127.0.0.1:17878/codebuddy/v1", APIKey: codeBuddyLocalAPIKey, APIBackend: "chat_completions"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := store.Create(profiles.Profile{
		Name: codeBuddyProfileName, Source: codeBuddyProfileSource, UpstreamFormat: "openai_chat",
		BaseURL: "http://127.0.0.1:17878/codebuddy/v1", APIKey: codeBuddyLocalAPIKey,
		DefaultModel: "codebuddy/hy3", Models: []profiles.ModelDef{
			{Name: "codebuddy/hy3", Model: "codebuddy/hy3", BaseURL: "http://127.0.0.1:17878/codebuddy/v1", APIKey: codeBuddyLocalAPIKey, APIBackend: "chat_completions"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	routingStore := routing.NewStore(filepath.Join(dir, "routing.json"))
	initial, err := routingStore.Initialize(store)
	if err != nil {
		t.Fatal(err)
	}
	var legacyRoute string
	for _, route := range initial.ModelRoutes {
		if route.ProviderID == legacy.ID {
			legacyRoute = route.Name
		}
	}
	if legacyRoute == "" || legacyRoute == "codebuddy/hy3" {
		t.Fatalf("expected qualified legacy route, got %q in %#v", legacyRoute, initial.ModelRoutes)
	}
	policy := initial.Policy
	policy.Default = legacyRoute
	policy.Subagents = routing.SubagentsPolicy{Explore: legacyRoute, Plan: legacyRoute}
	if _, err := routingStore.UpdatePolicy(policy); err != nil {
		t.Fatal(err)
	}

	s := &Server{Profiles: store, Routing: routingStore, CodeBuddy: &fakeCodeBuddyRunner{status: codebuddy.Status{Available: true, Models: []string{"hy3", "glm-5.2"}}}, ActualPort: 17878}
	if err := s.EnsureCodeBuddyProfile(); err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != canonical.ID || list[0].Source != codeBuddyProfileSource {
		t.Fatalf("legacy duplicate was not removed safely: %#v", list)
	}
	stored, err := routingStore.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Policy.Default != "codebuddy/hy3" || stored.Policy.WebSearch != "" || stored.Policy.Subagents.Explore != "codebuddy/hy3" || stored.Policy.Subagents.Plan != "codebuddy/hy3" {
		t.Fatalf("legacy selections were not translated: %#v", stored.Policy)
	}
}

func TestEnsureCodeBuddyProfilePreservesUserOwnedLookalike(t *testing.T) {
	store := profiles.NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	custom, err := store.Create(profiles.Profile{
		Name: codeBuddyProfileName, UpstreamFormat: "openai_chat",
		BaseURL: "https://example.test/codebuddy/v1", APIKey: "user-secret",
		DefaultModel: "codebuddy/hy3", Models: []profiles.ModelDef{{Name: "codebuddy/hy3", Model: "custom-upstream"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Profiles: store, CodeBuddy: &fakeCodeBuddyRunner{status: codebuddy.Status{Available: true, Models: []string{"hy3"}}}, ActualPort: 17878}
	if err := s.EnsureCodeBuddyProfile(); err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("user-owned lookalike should be preserved: %#v", list)
	}
	if got, err := store.Get(custom.ID); err != nil || got.APIKey != "user-secret" {
		t.Fatalf("custom profile changed or disappeared: %#v, err=%v", got, err)
	}
}

func TestCodeBuddyManagedProfileProjectsIntoRoutingAndUsesOrdinaryProviderIdentity(t *testing.T) {
	store := profiles.NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	s := &Server{Profiles: store, CodeBuddy: &fakeCodeBuddyRunner{status: codebuddy.Status{Available: true, Models: []string{"hy3"}}}, ActualPort: 18888}
	if err := s.EnsureCodeBuddyProfile(); err != nil {
		t.Fatal(err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := routing.ProjectWithPolicy(list, routing.RoutingPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Providers) != 1 || snapshot.Providers[0].Source != codeBuddyProfileSource || len(snapshot.ModelRoutes) != 1 {
		t.Fatalf("routing snapshot = %#v", snapshot)
	}
	route := snapshot.ModelRoutes[0]
	if route.Name != "codebuddy/hy3" || route.Model != "codebuddy/hy3" || route.APIBackend != "chat_completions" || route.BaseURL != "http://127.0.0.1:18888/codebuddy/v1" {
		t.Fatalf("CodeBuddy route = %#v", route)
	}
	profile, err := grokconfig.ProfileForRouting(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Models) != 1 || profile.Models[0].BaseURL != route.BaseURL {
		t.Fatalf("combined config profile = %#v", profile)
	}
	snippet, err := grokconfig.SnippetForRouting(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snippet, "[model.'codebuddy/hy3']") || !strings.Contains(snippet, route.BaseURL) {
		t.Fatalf("combined config is missing CodeBuddy model or local base URL:\n%s", snippet)
	}
	identity := providerFromProfile(list[0])
	if identity.Official || identity.ID != list[0].ID || identity.Backend != "chat_completions" || identity.BaseURL != route.BaseURL {
		t.Fatalf("provider identity is not an ordinary profile: %#v", identity)
	}
}
