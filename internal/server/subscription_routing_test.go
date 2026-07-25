package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
	"grok_switch/internal/switcher"
)

func TestEnsureSubscriptionProxyRoutesKeepsCombinedRouting(t *testing.T) {
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	regular, err := profileStore.Create(profiles.Profile{
		Name: "Regular", BaseURL: "https://regular.example", APIKey: "regular-key", DefaultModel: "regular",
		Models: []profiles.ModelDef{{Name: "regular", Model: "regular-upstream", APIBackend: "openai"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := profileStore.Create(profiles.Profile{
		Name: "Subscription", Source: "subscription-proxy:codex", BaseURL: subscriptionProxyUpstream, APIKey: "subscription-key", DefaultModel: "codex",
		Models: []profiles.ModelDef{{Name: "codex", Model: "codex-upstream", BaseURL: subscriptionProxyUpstream, APIBackend: "chat_completions"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	routingStore := routing.NewStore(filepath.Join(dir, "routing.json"))
	stored, err := routingStore.Initialize(profileStore)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[telemetry]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sw := &switcher.Switcher{ConfigPath: configPath, BackupsDir: filepath.Join(dir, "backups"), Profiles: profileStore}
	list, _ := profileStore.List()
	hydrated, err := routing.ProjectWithPolicy(list, stored.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.ApplyRouting(hydrated); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		ActualPort: 19091,
		Paths:      paths.Paths{GrokConfig: configPath, DataDir: dir},
		Profiles:   profileStore, Routing: routingStore, Switcher: sw,
	}
	if err := s.EnsureSubscriptionProxyRoutes(); err != nil {
		t.Fatal(err)
	}
	updatedSubscription, err := profileStore.Get(subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "http://127.0.0.1:19091/subscription-proxy/v1"
	if updatedSubscription.BaseURL != wantURL || updatedSubscription.Models[0].BaseURL != wantURL {
		t.Fatalf("subscription route = %#v, want %q", updatedSubscription, wantURL)
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	for _, wanted := range []string{"regular-upstream", "codex-upstream", wantURL} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("combined migrated config missing %q: %s", wanted, text)
		}
	}
	if strings.Contains(text, subscriptionProxyUpstream) {
		t.Fatalf("legacy subscription URL remains in config: %s", text)
	}
	persisted, err := routingStore.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Policy.Default == "" {
		t.Fatalf("routing policy lost default; regular=%s policy=%#v", regular.ID, persisted.Policy)
	}
}
