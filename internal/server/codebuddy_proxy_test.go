package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	created, err := profileStore.Create(profiles.Profile{
		Name: codebuddy.ProfileName, Source: codebuddy.SourceTag, APIKey: "ck_proxy",
		BaseURL: "http://127.0.0.1:1/codebuddy-proxy/v1", DefaultModel: "hy3",
		Models: []profiles.ModelDef{
			{Name: "hy3", Model: "hy3"},
			{Name: "deepseek-v4-flash", Model: "deepseek-v4-flash"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = created
	s := &Server{ActualPort: 1, Profiles: profileStore}
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
	// Add a second provider that also enables deepseek-v4-flash so the proxy
	// must advertise the @CodeBuddy alias instead of a bare duplicate id.
	if _, err := profileStore.Create(profiles.Profile{
		Name: "DeepSeek", APIKey: "ds", BaseURL: "https://api.deepseek.com", DefaultModel: "deepseek-v4-flash",
		Models: []profiles.ModelDef{{Name: "deepseek-v4-flash", Model: "deepseek-v4-flash"}},
	}); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/codebuddy-proxy/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rr = httptest.NewRecorder()
	s.handleCodeBuddyProxy(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	data, _ := body["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("proxy advertised %d models, want 2 subset: %#v", len(data), data)
	}
	ids := map[string]bool{}
	for _, item := range data {
		row, _ := item.(map[string]any)
		id, _ := row["id"].(string)
		ids[id] = true
	}
	if !ids["hy3"] || !ids["deepseek-v4-flash@CodeBuddy / WorkBuddy"] || ids["deepseek-v4-flash"] || ids["hy3-preview"] {
		t.Fatalf("proxy catalog = %#v, want aliased deepseek + bare hy3 only", ids)
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

func TestEnsureCodeBuddyProviderRepairsProviderQualifiedModelIDs(t *testing.T) {
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
	s := &Server{ActualPort: 19095, Paths: paths.Paths{GrokConfig: configPath, DataDir: dir}, Profiles: profileStore, Routing: routingStore, Switcher: sw}
	created, err := s.EnsureCodeBuddyProvider("ck_repair", false)
	if err != nil {
		t.Fatal(err)
	}
	broken := created
	broken.Models = []profiles.ModelDef{
		{Name: "hy3", Model: "hy3"},
		{Name: "deepseek-v4-flash@CodeBuddy / WorkBuddy", Model: "deepseek-v4-flash@CodeBuddy / WorkBuddy"},
	}
	broken.AvailableModels = []string{"hy3"}
	if _, err := profileStore.Update(created.ID, broken); err != nil {
		t.Fatal(err)
	}

	repaired, err := s.EnsureCodeBuddyProvider("ck_repair", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := codeBuddyModelNames(repaired.Models); !reflect.DeepEqual(got, []string{"hy3", "deepseek-v4-flash"}) {
		t.Fatalf("models = %#v", got)
	}
	if !reflect.DeepEqual(repaired.AvailableModels, []string{"hy3", "deepseek-v4-flash"}) {
		t.Fatalf("available_models = %#v", repaired.AvailableModels)
	}
}

func TestEnsureCodeBuddyProviderAddsExplicitDefaultToSubset(t *testing.T) {
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
	s := &Server{ActualPort: 19095, Paths: paths.Paths{GrokConfig: configPath, DataDir: dir}, Profiles: profileStore, Routing: routingStore, Switcher: sw}
	created, err := s.EnsureCodeBuddyProvider("ck_add", false)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := created
	trimmed.Models = []profiles.ModelDef{{Name: "hy3", Model: "hy3"}}
	trimmed.AvailableModels = []string{"hy3"}
	if _, err := profileStore.Update(created.ID, trimmed); err != nil {
		t.Fatal(err)
	}

	updated, err := s.EnsureCodeBuddyProviderOpts(CodeBuddyEnsureOptions{APIKey: "ck_add", DefaultModel: "deepseek-v4-pro"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DefaultModel != "deepseek-v4-pro" {
		t.Fatalf("default_model = %q", updated.DefaultModel)
	}
	if got := codeBuddyModelNames(updated.Models); !reflect.DeepEqual(got, []string{"hy3", "deepseek-v4-pro"}) {
		t.Fatalf("models = %#v", got)
	}
	for _, model := range updated.Models {
		if model.Name == "deepseek-v4-pro" && model.ContextWindow != 1000000 {
			t.Fatalf("deepseek-v4-pro context_window = %d", model.ContextWindow)
		}
	}
}

func TestEnsureCodeBuddyProviderRollsBackUpdatedProfileWhenRoutingFails(t *testing.T) {
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
	s := &Server{ActualPort: 19096, Paths: paths.Paths{GrokConfig: configPath, DataDir: dir}, Profiles: profileStore, Routing: routingStore, Switcher: sw}
	created, err := s.EnsureCodeBuddyProvider("ck_before", false)
	if err != nil {
		t.Fatal(err)
	}
	beforeProfiles, err := os.ReadFile(profileStore.Path())
	if err != nil {
		t.Fatal(err)
	}
	beforeRouting, err := os.ReadFile(routingStore.Path())
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	injected := fmt.Errorf("injected routing failure")
	oldReplace := replaceRoutingSnapshot
	replaceRoutingSnapshot = func(store *routing.Store, snapshot routing.Snapshot) (routing.Snapshot, error) {
		stored, err := store.Replace(snapshot)
		if err != nil {
			return routing.Snapshot{}, err
		}
		return stored, injected
	}
	defer func() { replaceRoutingSnapshot = oldReplace }()

	if _, err := s.EnsureCodeBuddyProviderOpts(CodeBuddyEnsureOptions{APIKey: "ck_after", DefaultModel: created.DefaultModel}); !errors.Is(err, injected) {
		t.Fatalf("EnsureCodeBuddyProviderOpts() error = %v, want injected", err)
	}
	assertFileBytesEqual(t, profileStore.Path(), beforeProfiles)
	assertFileBytesEqual(t, routingStore.Path(), beforeRouting)
	assertFileBytesEqual(t, configPath, beforeConfig)
}

func TestEnsureCodeBuddyProviderRollsBackNewProfileWhenRoutingFails(t *testing.T) {
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
	s := &Server{ActualPort: 19097, Paths: paths.Paths{GrokConfig: configPath, DataDir: dir}, Profiles: profileStore, Routing: routingStore, Switcher: sw}
	beforeRouting, err := os.ReadFile(routingStore.Path())
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	injected := fmt.Errorf("injected routing failure")
	oldReplace := replaceRoutingSnapshot
	replaceRoutingSnapshot = func(store *routing.Store, snapshot routing.Snapshot) (routing.Snapshot, error) {
		stored, err := store.Replace(snapshot)
		if err != nil {
			return routing.Snapshot{}, err
		}
		return stored, injected
	}
	defer func() { replaceRoutingSnapshot = oldReplace }()

	if _, err := s.EnsureCodeBuddyProvider("ck_new", false); !errors.Is(err, injected) {
		t.Fatalf("EnsureCodeBuddyProvider() error = %v, want injected", err)
	}
	list, err := profileStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("failed creation left profiles: %#v", list)
	}
	assertFileBytesEqual(t, routingStore.Path(), beforeRouting)
	assertFileBytesEqual(t, configPath, beforeConfig)
}

func TestManagedCodeBuddyProfileRejectsOrdinaryProfileMutation(t *testing.T) {
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
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got, err := profileStore.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != codebuddy.SourceTag || len(got.Models) != len(created.Models) {
		t.Fatalf("managed profile changed after rejected mutation: %#v", got)
	}
	// Startup/ensure must not recreate the full catalog.
	trimmed := got
	trimmed.Models = []profiles.ModelDef{
		{Name: "hy3", Model: "hy3", BaseURL: got.BaseURL, APIKey: "ck_preserve", APIBackend: "chat_completions"},
		{Name: "deepseek-v4-flash", Model: "deepseek-v4-flash", BaseURL: got.BaseURL, APIKey: "ck_preserve", APIBackend: "chat_completions"},
	}
	trimmed.AvailableModels = []string{"hy3", "deepseek-v4-flash"}
	trimmed.DefaultModel = "hy3"
	trimmed.DefaultReasoningEffort = "max"
	if _, err := profileStore.Update(created.ID, trimmed); err != nil {
		t.Fatal(err)
	}
	ensured, err := s.EnsureCodeBuddyProvider("ck_preserve", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ensured.Models) != 2 {
		t.Fatalf("ensure expanded models to %d: %#v", len(ensured.Models), ensured.Models)
	}
	names := map[string]bool{}
	for _, model := range ensured.Models {
		names[model.Name] = true
		if model.BaseURL != got.BaseURL || model.APIKey != "ck_preserve" || model.StreamToolCalls == nil || *model.StreamToolCalls {
			t.Fatalf("ensure did not restamp managed transport metadata: %#v", model)
		}
	}
	if !names["hy3"] || !names["deepseek-v4-flash"] {
		t.Fatalf("ensure lost user subset: %#v", ensured.Models)
	}
}
