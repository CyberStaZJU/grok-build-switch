package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/profiles"
)

func TestInitializePersistsOnlyCatalogAndPolicy(t *testing.T) {
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	if _, err := profileStore.Create(profiles.Profile{
		Name:         "Acme",
		BaseURL:      "https://one.example/v1",
		APIKey:       "key-one",
		DefaultModel: "shared",
		Models: []profiles.ModelDef{{
			Name:         "shared",
			Model:        "upstream-one",
			APIBackend:   "responses",
			ExtraHeaders: map[string]string{"Authorization": "secret-header"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	second, err := profileStore.Create(profiles.Profile{
		Name:    "Acme",
		BaseURL: "https://two.example/v1",
		APIKey:  "key-two",
		Models:  []profiles.ModelDef{{Name: "shared", Model: "upstream-two", APIBackend: "chat_completions"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "routing.json")
	store := NewStore(path)
	snapshot, err := store.Initialize(profileStore)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Providers) != 2 || len(snapshot.ModelRoutes) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Providers[0].Name != "Acme" || snapshot.Providers[1].Name != "Acme (2)" {
		t.Fatalf("provider conflict names = %#v", snapshot.Providers)
	}
	if snapshot.ModelRoutes[0].Name != "shared@Acme" || snapshot.ModelRoutes[1].Name != "shared@Acme (2)" {
		t.Fatalf("route conflict names = %#v", snapshot.ModelRoutes)
	}
	if snapshot.ModelRoutes[0].ProfileModel != "shared" || snapshot.ModelRoutes[1].ProviderID != second.ID {
		t.Fatalf("route references = %#v", snapshot.ModelRoutes)
	}
	if snapshot.Policy.Default != "shared@Acme" {
		t.Fatalf("policy = %#v", snapshot.Policy)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"key-one", "key-two", "secret-header", "Authorization", "https://one.example/v1", "https://two.example/v1"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("routing.json leaked %q:\n%s", secret, data)
		}
	}
	for _, key := range []string{`"api_key"`, `"extra_headers"`, `"base_url"`} {
		if strings.Contains(string(data), key) {
			t.Fatalf("routing.json contains runtime field %s:\n%s", key, data)
		}
	}
	marshaled, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(marshaled), "api_key") || strings.Contains(string(marshaled), "extra_headers") {
		t.Fatalf("default JSON API exposed credentials: %s", marshaled)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("routing.json mode = %o, want 600", got)
	}
}

func TestHydrateUsesLatestProfileCredentials(t *testing.T) {
	profile := profiles.Normalize(profiles.Profile{
		ID:      "p1",
		Name:    "Provider",
		BaseURL: "https://latest.example/v1",
		APIKey:  "latest-key",
		Models: []profiles.ModelDef{{
			Name:         "m",
			Model:        "upstream-m",
			APIBackend:   "responses",
			ExtraHeaders: map[string]string{"X-Secret": "header-secret"},
		}},
	})
	snapshot := Project([]profiles.Profile{profile})
	if snapshot.Providers[0].APIKey != "" || snapshot.ModelRoutes[0].ExtraHeaders != nil {
		t.Fatalf("Project returned hydrated credentials: %#v", snapshot)
	}
	hydrated, err := Hydrate(snapshot, []profiles.Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	if hydrated.Providers[0].BaseURL != profile.BaseURL || hydrated.Providers[0].APIKey != profile.APIKey {
		t.Fatalf("provider runtime data = %#v", hydrated.Providers[0])
	}
	route := hydrated.ModelRoutes[0]
	if route.Model != "upstream-m" || route.BaseURL != profile.BaseURL || route.APIKey != profile.APIKey || route.ExtraHeaders["X-Secret"] != "header-secret" {
		t.Fatalf("hydrated route = %#v", route)
	}
	marshaled, err := json.Marshal(hydrated)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"latest-key", "header-secret", "X-Secret", "https://latest.example/v1"} {
		if strings.Contains(string(marshaled), secret) {
			t.Fatalf("hydrated snapshot JSON leaked %q: %s", secret, marshaled)
		}
	}
}

func TestInitializeRejectsQuarantinedProfilesWithoutCreatingRouting(t *testing.T) {
	dir := t.TempDir()
	profilesPath := filepath.Join(dir, "profiles.json")
	original := []byte(`[
  {"id":"duplicate","name":"first","models":[{"name":"m1","model":"m1"}]},
  {"id":"duplicate","name":"second","models":[{"name":"m2","model":"m2"}]}
]
`)
	if err := os.WriteFile(profilesPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	routingPath := filepath.Join(dir, "routing.json")
	_, err := NewStore(routingPath).Initialize(profiles.NewStore(profilesPath))
	if err == nil || !strings.Contains(err.Error(), `duplicate profile id "duplicate"`) {
		t.Fatalf("Initialize() error = %v", err)
	}
	if _, statErr := os.Stat(routingPath); !os.IsNotExist(statErr) {
		t.Fatalf("routing.json exists after quarantined profiles: %v", statErr)
	}
	matches, globErr := filepath.Glob(profilesPath + ".corrupt-*.bak")
	if globErr != nil || len(matches) != 1 {
		t.Fatalf("profile quarantine backups = %#v, err = %v", matches, globErr)
	}
	got, readErr := os.ReadFile(matches[0])
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("quarantine changed original bytes\nwant=%q\ngot=%q", original, got)
	}
}

func TestInitializeIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	created, err := profileStore.Create(profiles.Profile{Name: "Initial", DefaultModel: "m", Models: []profiles.ModelDef{{Name: "m", Model: "m"}}})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(dir, "routing.json"))
	first, err := store.Initialize(profileStore)
	if err != nil {
		t.Fatal(err)
	}
	first.Providers[0].Name = "mutated"
	if _, err := profileStore.Update(created.ID, profiles.Profile{Name: "Changed", DefaultModel: "other", Models: []profiles.ModelDef{{Name: "other", Model: "other"}}}); err != nil {
		t.Fatal(err)
	}
	second, err := store.Initialize(profileStore)
	if err != nil {
		t.Fatal(err)
	}
	if second.Providers[0].Name != "Initial" || second.ModelRoutes[0].Name != "m" {
		t.Fatalf("Initialize rewrote existing routing: %#v", second)
	}
}

func TestReadRewritesLegacyCredentialFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.json")
	legacy := `{
  "version": 1,
  "providers": [{"id":"p","name":"P","profile_id":"profile-p","base_url":"https://secret.example/v1","api_key":"legacy-key"}],
  "model_routes": [{"id":"p:m","name":"m","provider_id":"p","model":"upstream-m","api_backend":"responses","api_key":"model-key","extra_headers":{"Authorization":"Bearer secret"}}],
  "policy": {"default":"m"},
  "updated_at": "2026-01-01T00:00:00Z"
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewStore(path).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ModelRoutes[0].ProfileModel != "m" {
		t.Fatalf("legacy profile model migration = %#v", snapshot.ModelRoutes[0])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"legacy-key", "model-key", "Bearer secret", "api_key", "extra_headers", "secret.example"} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("rewritten routing.json still contains %q:\n%s", leaked, data)
		}
	}
}

func TestUpdatePolicyPersistsAndValidates(t *testing.T) {
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	if _, err := profileStore.Create(profiles.Profile{Name: "P", Models: []profiles.ModelDef{{Name: "a", Model: "a"}, {Name: "b", Model: "b"}}}); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(dir, "routing.json"))
	if _, err := store.Initialize(profileStore); err != nil {
		t.Fatal(err)
	}
	policy := RoutingPolicy{
		Default:                "b",
		DefaultReasoningEffort: "medium",
		WebSearch:              "a",
		Subagents:              SubagentsPolicy{Explore: "a", Plan: "b"},
	}
	updated, err := store.UpdatePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Policy != policy {
		t.Fatalf("updated policy = %#v, want %#v", updated.Policy, policy)
	}
	reloaded, err := NewStore(store.Path()).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Policy != policy {
		t.Fatalf("reloaded policy = %#v, want %#v", reloaded.Policy, policy)
	}
	if _, err := store.UpdatePolicy(RoutingPolicy{Default: "missing"}); err == nil {
		t.Fatal("expected unknown route validation error")
	}
	after, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Policy != policy {
		t.Fatalf("invalid update changed persisted policy: %#v", after.Policy)
	}
}
