package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/codebuddy"
	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
	"grok_switch/internal/switcher"
)

func TestEnsureCodeBuddyProviderActivatesHy3Default(t *testing.T) {
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	routingStore := routing.NewStore(filepath.Join(dir, "routing.json"))
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[telemetry]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sw := &switcher.Switcher{ConfigPath: configPath, Profiles: profileStore}
	if _, err := routingStore.Initialize(profileStore); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		ActualPort: 19092,
		Paths:      paths.Paths{GrokConfig: configPath, DataDir: dir},
		Profiles:   profileStore,
		Routing:    routingStore,
		Switcher:   sw,
	}

	profile, err := s.EnsureCodeBuddyProvider("ck_test_key", true)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Source != codebuddy.SourceTag {
		t.Fatalf("source = %q", profile.Source)
	}
	if profile.DefaultModel != "hy3" {
		t.Fatalf("default_model = %q", profile.DefaultModel)
	}
	wantURL := "http://127.0.0.1:19092/codebuddy-proxy/v1"
	if profile.BaseURL != wantURL {
		t.Fatalf("base_url = %q, want %q", profile.BaseURL, wantURL)
	}

	snap, err := routingStore.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.ActiveProviderID != profile.ID {
		t.Fatalf("active_provider_id = %q, want %q", snap.ActiveProviderID, profile.ID)
	}
	policy := snap.ProviderPolicies[profile.ID]
	if policy.Default != profile.ID+":hy3" {
		t.Fatalf("policy.default = %q", policy.Default)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, needle := range []string{"default = 'hy3'", wantURL, "model = 'hy3'", "ck_test_key", "stream_tool_calls = false"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("config missing %q:\n%s", needle, text)
		}
	}
}

func TestCodeBuddyProxyModelsLoopback(t *testing.T) {
	s := &Server{ActualPort: 1}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/codebuddy-proxy/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	s.handleCodeBuddyProxy(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	data, _ := body["data"].([]any)
	if len(data) == 0 {
		t.Fatal("expected models")
	}
}

func TestEnsureCodeBuddyProviderAdoptsLegacyLookalikeInsteadOfDuplicating(t *testing.T) {
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	routingStore := routing.NewStore(filepath.Join(dir, "routing.json"))
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[telemetry]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sw := &switcher.Switcher{ConfigPath: configPath, Profiles: profileStore}
	if _, err := routingStore.Initialize(profileStore); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		ActualPort: 19093,
		Paths:      paths.Paths{GrokConfig: configPath, DataDir: dir},
		Profiles:   profileStore,
		Routing:    routingStore,
		Switcher:   sw,
	}

	legacy, err := profileStore.Create(profiles.Profile{
		Name:           codebuddy.ProfileName,
		UpstreamFormat: "openai_chat",
		BaseURL:        "http://127.0.0.1:17878/codebuddy-proxy/v1",
		APIKey:         "ck_legacy",
		DefaultModel:   "hy3",
		Models: []profiles.ModelDef{{
			Name: "hy3", Model: "hy3", BaseURL: "http://127.0.0.1:17878/codebuddy-proxy/v1", APIKey: "ck_legacy",
			APIBackend: "chat_completions",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Source != "" {
		t.Fatalf("legacy source should start empty, got %q", legacy.Source)
	}

	updated, err := s.EnsureCodeBuddyProvider("ck_new_key", false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != legacy.ID {
		t.Fatalf("expected adopt id %s, got %s", legacy.ID, updated.ID)
	}
	if updated.Source != codebuddy.SourceTag {
		t.Fatalf("source = %q", updated.Source)
	}
	list, err := profileStore.List()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, p := range list {
		if p.Name == codebuddy.ProfileName || p.Source == codebuddy.SourceTag || isCodeBuddyProxyBaseURL(p.BaseURL) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one CodeBuddy profile, got %d", count)
	}
}

func TestEnsureCodeBuddyProviderDoesNotCreateWhenTaggedExists(t *testing.T) {
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	routingStore := routing.NewStore(filepath.Join(dir, "routing.json"))
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[telemetry]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sw := &switcher.Switcher{ConfigPath: configPath, Profiles: profileStore}
	if _, err := routingStore.Initialize(profileStore); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		ActualPort: 19094,
		Paths:      paths.Paths{GrokConfig: configPath, DataDir: dir},
		Profiles:   profileStore,
		Routing:    routingStore,
		Switcher:   sw,
	}
	first, err := s.EnsureCodeBuddyProvider("ck_one", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnsureCodeBuddyProvider("ck_two", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("created duplicate: %s vs %s", first.ID, second.ID)
	}
	list, err := profileStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("profile count = %d", len(list))
	}
}

func TestProfileUpdatePreservesCodeBuddySource(t *testing.T) {
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	routingStore := routing.NewStore(filepath.Join(dir, "routing.json"))
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[telemetry]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sw := &switcher.Switcher{ConfigPath: configPath, Profiles: profileStore}
	if _, err := routingStore.Initialize(profileStore); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		ActualPort: 19095,
		Paths:      paths.Paths{GrokConfig: configPath, DataDir: dir},
		Profiles:   profileStore,
		Routing:    routingStore,
		Switcher:   sw,
	}
	created, err := s.EnsureCodeBuddyProvider("ck_preserve", false)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"name":"CodeBuddy / WorkBuddy","upstream_format":"openai_chat","base_url":"http://127.0.0.1:19095/codebuddy-proxy/v1","api_key":"ck_preserve","default_model":"hy3","models":[{"name":"hy3","model":"hy3","base_url":"http://127.0.0.1:19095/codebuddy-proxy/v1","api_key":"ck_preserve","api_backend":"chat_completions"}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/profiles/"+created.ID, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1"
	rr := httptest.NewRecorder()
	s.handleProfileByID(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got, err := profileStore.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != codebuddy.SourceTag {
		t.Fatalf("source cleared on UI update: %q", got.Source)
	}
}
