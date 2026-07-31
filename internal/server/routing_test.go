package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	grokconfig "grok_switch/internal/config"
	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
	"grok_switch/internal/settings"
	"grok_switch/internal/switcher"
)

func TestStatusDoesNotExposeActiveProfileCredentials(t *testing.T) {
	s := newRoutingTestServer(t)
	s.Settings = settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	request := loopbackRequest(http.MethodGet, "/api/status", "")
	response := httptest.NewRecorder()
	s.handleStatus(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, secret := range []string{"provider-secret-one", "provider-secret-two", "model-secret-two", "X-Secret", "header-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("status response leaked %q: %s", secret, body)
		}
	}
}

func TestRoutingGETIncludesOfficialModelsWhenLoggedIn(t *testing.T) {
	s := newRoutingTestServer(t)
	if err := os.MkdirAll(s.Paths.GrokHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Paths.GrokHome, "auth.json"), []byte(`{"type":"xai","access_token":"never-return-this"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	request := loopbackRequest(http.MethodGet, "/api/routing", "")
	response := httptest.NewRecorder()
	s.handleRouting(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var snapshot routingSnapshotDTO
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.OfficialLoggedIn || len(snapshot.OfficialModels) == 0 || snapshot.OfficialModels[0].Name != "grok-4.5" {
		t.Fatalf("official routing metadata = %#v", snapshot)
	}
	if strings.Contains(response.Body.String(), "never-return-this") {
		t.Fatal("routing response leaked official auth contents")
	}
}

func TestOfficialActivateWithoutLoginLeavesStateUnchanged(t *testing.T) {
	s := newRoutingTestServer(t)
	beforeConfig, err := os.ReadFile(s.Switcher.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeRouting, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	oldStart := startGrokLogin
	started := false
	startGrokLogin = func() error { started = true; return nil }
	defer func() { startGrokLogin = oldStart }()

	response := httptest.NewRecorder()
	s.handleOfficialActivate(response, loopbackRequest(http.MethodPost, "/api/official/activate", ""))
	if response.Code != http.StatusOK || !started {
		t.Fatalf("status=%d started=%v body=%s", response.Code, started, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["login_required"] != true || body["switched"] != false {
		t.Fatalf("response = %#v", body)
	}
	afterConfig, err := os.ReadFile(s.Switcher.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	afterRouting, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if string(afterConfig) != string(beforeConfig) || afterRouting.Policy != beforeRouting.Policy || afterRouting.Version != beforeRouting.Version {
		t.Fatalf("state changed while login pending: before=%#v after=%#v", beforeRouting, afterRouting)
	}
}

func TestOfficialRoutingTransactionRejectsEmptyDefault(t *testing.T) {
	s := newRoutingTestServer(t)
	before, err := os.ReadFile(s.Switcher.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.applyRoutingPolicyTransaction(mustProfiles(t, s), routing.RoutingPolicy{Official: true}); err == nil {
		t.Fatal("expected empty official default to be rejected")
	}
	after, err := os.ReadFile(s.Switcher.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("invalid official policy changed config:\n%s", after)
	}
}

func TestResolveRoutingModelIncludesExecutableNativeOfficialRoute(t *testing.T) {
	s := newRoutingTestServer(t)
	if err := os.MkdirAll(s.Paths.GrokHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Paths.GrokHome, "auth.json"), []byte(`{"type":"xai","access_token":"native-access"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := routing.Snapshot{Policy: routing.RoutingPolicy{Official: true, Default: "grok-4.5"}}
	route, ok := s.resolveRoutingModel(snapshot, snapshot.Policy.Default)
	if !ok || route.Model != "grok-4.5" || route.APIBackend != "responses" || route.BaseURL != "https://cli-chat-proxy.grok.com/v1" || route.APIKey != "native-access" || !route.SupportsReasoningEffort {
		t.Fatalf("official route = %#v, %v", route, ok)
	}
	for key, want := range map[string]string{"X-XAI-Token-Auth": "xai-grok-cli", "x-grok-client-version": "0.2.93", "User-Agent": "xai-grok-workspace/0.2.93"} {
		if route.ExtraHeaders[key] != want {
			t.Fatalf("official header %s = %q, want %q", key, route.ExtraHeaders[key], want)
		}
	}
}

func TestResolveRoutingModelRejectsExpiredNativeOfficialCredential(t *testing.T) {
	s := newRoutingTestServer(t)
	if err := os.MkdirAll(s.Paths.GrokHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Paths.GrokHome, "auth.json"), []byte(`{"access_token":"expired","expires_at":"2000-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := routing.Snapshot{Policy: routing.RoutingPolicy{Official: true, Default: "grok-4.5"}}
	if route, ok := s.resolveRoutingModel(snapshot, snapshot.Policy.Default); ok {
		t.Fatalf("unexpected expired official route: %#v", route)
	}
}

func TestRoutingPolicyPUTSelectsOfficialGrokModel(t *testing.T) {
	s := newRoutingTestServer(t)
	if err := os.MkdirAll(s.Paths.GrokHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Paths.GrokHome, "auth.json"), []byte(`{"type":"xai","access_token":"official-access"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := `{"official":true,"default":"grok-4.5","default_reasoning_effort":"high","web_search":"grok-4.5","subagents":{"explore":"grok-4.5","plan":"grok-4.5"}}`
	request := loopbackRequest(http.MethodPut, "/api/routing/policy", payload)
	response := httptest.NewRecorder()
	s.handleRoutingPolicy(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Policy.Official || stored.Policy.Default != "grok-4.5" || stored.Policy.WebSearch != "grok-4.5" {
		t.Fatalf("stored official policy = %#v", stored.Policy)
	}
	config, err := os.ReadFile(s.Paths.GrokConfig)
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	for _, want := range []string{`default = 'grok-4.5'`, `web_search = 'grok-4.5'`, `explore = 'grok-4.5'`, `plan = 'grok-4.5'`} {
		if !strings.Contains(text, want) {
			t.Fatalf("official config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "private-one.example") || strings.Contains(text, "provider-secret-one") {
		t.Fatalf("official config retained custom provider data:\n%s", text)
	}
}

func TestStatusReportsOfficialModelPins(t *testing.T) {
	s := newRoutingTestServer(t)
	s.Settings = settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	if err := os.MkdirAll(s.Paths.GrokHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Paths.GrokHome, "auth.json"), []byte(`{"type":"xai","access_token":"official-access"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	profileList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	policy := routing.RoutingPolicy{Official: true, Default: "grok-4.5", WebSearch: "grok-4.5", Subagents: routing.SubagentsPolicy{Explore: "grok-4.5", Plan: "grok-4.5"}}
	if _, err := s.applyRoutingPolicyTransaction(profileList, policy); err != nil {
		t.Fatal(err)
	}
	request := loopbackRequest(http.MethodGet, "/api/status", "")
	response := httptest.NewRecorder()
	s.handleStatus(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"default_model", "web_search_model", "explore_model", "plan_model"} {
		if status[field] != "grok-4.5" {
			t.Fatalf("%s = %#v, want grok-4.5", field, status[field])
		}
	}
}

func TestRoutingGETReturnsSafeMultiProviderCatalog(t *testing.T) {
	s := newRoutingTestServer(t)
	request := loopbackRequest(http.MethodGet, "/api/routing", "")
	response := httptest.NewRecorder()
	s.handleRouting(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, secret := range []string{"provider-secret-one", "model-secret-two", "X-Secret", "header-secret", "https://private-one.example"} {
		if strings.Contains(body, secret) {
			t.Fatalf("routing response leaked %q: %s", secret, body)
		}
	}
	var snapshot routingSnapshotDTO
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Providers) != 2 || len(snapshot.ModelRoutes) != 2 {
		t.Fatalf("providers=%d routes=%d, want 2/2", len(snapshot.Providers), len(snapshot.ModelRoutes))
	}
	backends := map[string]string{}
	for _, model := range snapshot.ModelRoutes {
		backends[model.Model] = model.APIBackend
	}
	if backends["upstream-one"] != "openai" || backends["upstream-two"] != "anthropic" {
		t.Fatalf("safe upstream metadata = %#v", backends)
	}
}

func TestRoutingPolicyPUTAppliesCombinedConfig(t *testing.T) {
	s := newRoutingTestServer(t)
	catalog, _, err := s.currentRouting()
	if err != nil {
		t.Fatal(err)
	}
	policy := routing.RoutingPolicy{
		Default:   catalog.ModelRoutes[0].Name,
		WebSearch: catalog.ModelRoutes[1].Name,
		Subagents: routing.SubagentsPolicy{Explore: catalog.ModelRoutes[0].Name, Plan: catalog.ModelRoutes[1].Name},
	}
	// Use a raw JSON payload to explicitly include all fields (including zero-values)
	// so the merge replaces them correctly.
	payload := []byte(`{"official":false,"default":"` + policy.Default + `","default_reasoning_effort":"","web_search":"` + policy.WebSearch + `","subagents":{"explore":"` + policy.Subagents.Explore + `","plan":"` + policy.Subagents.Plan + `"}}`)
	request := loopbackRequest(http.MethodPut, "/api/routing/policy", string(payload))
	response := httptest.NewRecorder()
	s.handleRoutingPolicy(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Policy != policy {
		t.Fatalf("stored policy = %#v, want %#v", stored.Policy, policy)
	}
	profileList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	hydrated, err := routing.ProjectWithPolicy(profileList, policy)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := grokconfig.CurrentMatchesRouting(s.Paths.GrokConfig, hydrated)
	if err != nil {
		t.Fatal(err)
	}
	if !matches {
		t.Fatal("combined config does not match saved routing policy")
	}
	config, err := os.ReadFile(s.Paths.GrokConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "upstream-one") || !strings.Contains(string(config), "upstream-two") {
		t.Fatalf("combined config missing provider models: %s", config)
	}
}

func TestRoutingPolicyPUTCanClearSubagentRoutes(t *testing.T) {
	s := newRoutingTestServer(t)
	catalog, _, err := s.currentRouting()
	if err != nil {
		t.Fatal(err)
	}
	initial := catalog.Policy
	initial.Subagents = routing.SubagentsPolicy{Explore: catalog.ModelRoutes[0].Name, Plan: catalog.ModelRoutes[1].Name}
	if _, err := s.applyRoutingPolicyTransaction(mustProfiles(t, s), initial); err != nil {
		t.Fatal(err)
	}

	request := loopbackRequest(http.MethodPut, "/api/routing/policy", `{"subagents":{"explore":"","plan":""}}`)
	response := httptest.NewRecorder()
	s.handleRoutingPolicy(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Policy.Subagents.Explore != "" || stored.Policy.Subagents.Plan != "" {
		t.Fatalf("subagent routes were not cleared: %#v", stored.Policy.Subagents)
	}
}

func mustProfiles(t *testing.T, s *Server) []profiles.Profile {
	t.Helper()
	items, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestRoutingPolicyCanLeaveOfficialModeConsistently(t *testing.T) {
	s := newRoutingTestServer(t)
	profilesList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.applyRoutingPolicyTransaction(profilesList, routing.RoutingPolicy{Official: true, Default: defaultOfficialRoutingModels[0].Name}); err != nil {
		t.Fatal(err)
	}
	catalog := routing.Project(profilesList)
	// Explicitly send official:false to leave official mode.
	payload := []byte(`{"official":false,"default":"` + catalog.ModelRoutes[0].Name + `"}`)
	request := loopbackRequest(http.MethodPut, "/api/routing/policy", string(payload))
	response := httptest.NewRecorder()
	s.handleRoutingPolicy(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	// Verify the routing policy was updated correctly (no active profile concept).
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Policy.Official || stored.Policy.Default != catalog.ModelRoutes[0].Name {
		t.Fatalf("routing policy = %#v, want default %q", stored.Policy, catalog.ModelRoutes[0].Name)
	}
}

func TestRoutingPolicySwitchesProvidersAndKeepsCombinedConfig(t *testing.T) {
	s := newRoutingTestServer(t)
	profileList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	var target profiles.Profile
	for _, profile := range profileList {
		if profile.Name == "Two" {
			target = profile
			break
		}
	}
	catalog := routing.Project(profileList)
	wantDefault, ok := routeForProfile(catalog, target.ID, target.DefaultModel)
	if !ok {
		t.Fatal("default route not found for target profile")
	}
	policy := routing.RoutingPolicy{Default: wantDefault.Name, DefaultReasoningEffort: target.DefaultReasoningEffort}
	payload, _ := json.Marshal(policy)
	request := loopbackRequest(http.MethodPut, "/api/routing/policy", string(payload))
	response := httptest.NewRecorder()
	s.handleRoutingPolicy(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Policy.Default != wantDefault.Name {
		t.Fatalf("default = %q, want route %#v", stored.Policy.Default, wantDefault)
	}
	// Verify routing config matches.
	_, hydrated, routingErr := s.currentRouting()
	if routingErr != nil {
		t.Fatal(routingErr)
	}
	routingMatches, routingErr := grokconfig.CurrentMatchesRouting(s.Paths.GrokConfig, hydrated)
	if routingErr != nil || !routingMatches {
		t.Fatalf("routing matches=%v err=%v", routingMatches, routingErr)
	}
	config, err := os.ReadFile(s.Paths.GrokConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"upstream-one", "upstream-two"} {
		if !strings.Contains(string(config), model) {
			t.Fatalf("activate removed %q from combined config: %s", model, config)
		}
	}
}

func TestProfileDeleteRepairsRoutingPolicyAndConfig(t *testing.T) {
	s := newRoutingTestServer(t)
	profileList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	var active profiles.Profile
	for _, profile := range profileList {
		active = profile
		break
	}
	request := loopbackRequest(http.MethodDelete, "/api/profiles/"+active.ID, "")
	response := httptest.NewRecorder()
	s.handleProfileByID(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	hydrated, err := routing.ProjectWithPolicy(remaining, stored.Policy)
	if err != nil {
		t.Fatalf("routing remained invalid after profile delete: %v", err)
	}
	matches, err := grokconfig.CurrentMatchesRouting(s.Paths.GrokConfig, hydrated)
	if err != nil || !matches {
		t.Fatalf("combined config matches=%v err=%v", matches, err)
	}
}

func TestCurrentRoutingRepairsStalePersistedPolicy(t *testing.T) {
	s := newRoutingTestServer(t)
	profileList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	catalog := routing.Project(profileList)
	if len(catalog.ModelRoutes) < 2 {
		t.Fatal("expected two routes")
	}
	if _, err := s.Routing.UpdatePolicy(routing.RoutingPolicy{Default: catalog.ModelRoutes[0].Name}); err != nil {
		t.Fatal(err)
	}
	provider, ok := catalog.Provider(catalog.ModelRoutes[0].ProviderID)
	if !ok {
		t.Fatal("default provider missing")
	}
	if err := s.Profiles.Delete(provider.ProfileID); err != nil {
		t.Fatal(err)
	}
	_, hydrated, err := s.currentRouting()
	if err != nil {
		t.Fatalf("currentRouting did not repair stale policy: %v", err)
	}
	if hydrated.Policy.Default == catalog.ModelRoutes[0].Name || hydrated.Policy.Default == "" {
		t.Fatalf("repaired default = %q", hydrated.Policy.Default)
	}
}

func TestValidateRoutingReasoningEffortAllowsNoneForCustomAndOfficial(t *testing.T) {
	custom := routing.Snapshot{
		Providers:   []routing.Provider{{ID: "p"}},
		ModelRoutes: []routing.ModelRoute{{Name: "plain", ProviderID: "p"}},
		Policy:      routing.RoutingPolicy{Default: "plain", DefaultReasoningEffort: "none"},
	}
	if err := validateRoutingReasoningEffort(custom); err != nil {
		t.Fatalf("custom none rejected: %v", err)
	}
	official := routing.Snapshot{Policy: routing.RoutingPolicy{
		Official: true, Default: defaultOfficialRoutingModels[0].Name, DefaultReasoningEffort: "none",
	}}
	if err := validateRoutingReasoningEffort(official); err != nil {
		t.Fatalf("official none rejected: %v", err)
	}
}

func TestRoutingPolicyPUTRejectsUnsupportedReasoningEffort(t *testing.T) {
	s := newRoutingTestServer(t)
	catalog, _, err := s.currentRouting()
	if err != nil {
		t.Fatal(err)
	}
	request := loopbackRequest(http.MethodPut, "/api/routing/policy", `{"default":"`+catalog.ModelRoutes[0].Name+`","default_reasoning_effort":"xhigh"}`)
	response := httptest.NewRecorder()
	s.handleRoutingPolicy(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "不支持推理强度") {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestRoutingPolicyPUTRejectsInvalidPolicyWithoutChangingState(t *testing.T) {
	s := newRoutingTestServer(t)
	beforeStore, err := os.ReadFile(s.Routing.Path())
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(s.Paths.GrokConfig)
	if err != nil {
		t.Fatal(err)
	}
	request := loopbackRequest(http.MethodPut, "/api/routing/policy", `{"default":"missing-route"}`)
	response := httptest.NewRecorder()
	s.handleRoutingPolicy(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	afterStore, _ := os.ReadFile(s.Routing.Path())
	afterConfig, _ := os.ReadFile(s.Paths.GrokConfig)
	if string(afterStore) != string(beforeStore) || string(afterConfig) != string(beforeConfig) {
		t.Fatal("invalid policy changed routing store or config")
	}
}

func TestRoutingPolicyPUTRejectsRemoteMutation(t *testing.T) {
	s := newRoutingTestServer(t)
	settingsStore := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	current := settings.Default()
	current.LANAccessEnabled = true
	if _, err := settingsStore.Update(current); err != nil {
		t.Fatal(err)
	}
	s.Settings = settingsStore
	mux := http.NewServeMux()
	s.routes(mux)
	request := httptest.NewRequest(http.MethodPut, "http://192.168.1.10/api/routing/policy", strings.NewReader(`{}`))
	request.RemoteAddr = "192.168.1.20:40000"
	request.Host = "192.168.1.10"
	request.Header.Set("Origin", "http://192.168.1.10")
	response := httptest.NewRecorder()
	s.withAccess(mux).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 before pairing; body=%s", response.Code, response.Body.String())
	}

	// A paired/authorized remote request can reach the route, but the mutation
	// itself still has an explicit loopback-only guard.
	response = httptest.NewRecorder()
	s.handleRoutingPolicy(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("direct handler status = %d, want 403; body=%s", response.Code, response.Body.String())
	}
}

func newRoutingTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	fixtures := []profiles.Profile{
		{
			Name: "One", BaseURL: "https://private-one.example", APIKey: "provider-secret-one", DefaultModel: "shared",
			Models: []profiles.ModelDef{{Name: "shared", Model: "upstream-one", APIBackend: "openai"}},
		},
		{
			Name: "Two", BaseURL: "https://private-two.example", APIKey: "provider-secret-two", DefaultModel: "shared",
			Models: []profiles.ModelDef{{Name: "shared", Model: "upstream-two", APIBackend: "anthropic", APIKey: "model-secret-two", ExtraHeaders: map[string]string{"X-Secret": "header-secret"}}},
		},
	}
	for _, profile := range fixtures {
		if _, err := profileStore.Create(profile); err != nil {
			t.Fatal(err)
		}
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
	sw := &switcher.Switcher{ConfigPath: configPath, Profiles: profileStore}
	profileList, _ := profileStore.List()
	hydrated, err := routing.ProjectWithPolicy(profileList, stored.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.ApplyRouting(hydrated); err != nil {
		t.Fatal(err)
	}
	return &Server{
		Paths:    paths.Paths{GrokConfig: configPath, DataDir: dir, GrokHome: filepath.Join(dir, "grok")},
		Profiles: profileStore, Routing: routingStore, Switcher: sw,
	}
}
