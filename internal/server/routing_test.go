package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/agentbridge"
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

func TestActiveProviderIdentityUsesRoutingDefaultProvider(t *testing.T) {
	s := newRoutingTestServer(t)
	profileList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	catalog := routing.Project(profileList)
	var target routing.ModelRoute
	for _, route := range catalog.ModelRoutes {
		provider, _ := catalog.Provider(route.ProviderID)
		if provider.ProfileID != profileList[0].ID {
			target = route
			break
		}
	}
	if target.Name == "" {
		t.Fatal("target route not found")
	}
	policy := stored.Policy
	policy.Default = target.Name
	if _, err := s.applyRoutingPolicyTransaction(profileList, policy); err != nil {
		t.Fatal(err)
	}
	identity := s.activeProviderIdentity()
	if identity.ID != target.ProviderID || identity.Model != target.Name {
		t.Fatalf("identity = %#v, want provider=%q model=%q", identity, target.ProviderID, target.Name)
	}
}

func TestProfileActivationHandoffUsesHydratedModelIdentity(t *testing.T) {
	s := newRoutingTestServer(t)
	profileList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	// Use the second profile as target to trigger a provider handoff.
	if len(profileList) < 2 {
		t.Fatal("need at least 2 profiles for handoff test")
	}
	target := profileList[1]
	target.Models[0].BaseURL = "https://model-specific.example/v1"
	if _, err := s.Profiles.Update(target.ID, target); err != nil {
		t.Fatal(err)
	}
	agent := &sessionSwitchAgentFake{
		status:   agentbridge.Status{Running: true, State: "ready", SessionID: "session-a", Cwd: t.TempDir(), Model: "shared"},
		history:  agentbridge.SessionHistory{Session: agentbridge.SessionSummary{ID: "session-a", LogicalSessionID: "logical-a"}},
		transfer: "safe text",
	}
	s.Agent = agent
	catalog := routing.Project(profileList)
	defaultRoute, ok := routeForProfile(catalog, target.ID, target.DefaultModel)
	if !ok {
		t.Fatal("default route not found for target profile")
	}
	policy := routing.RoutingPolicy{Default: defaultRoute.Name, DefaultReasoningEffort: target.DefaultReasoningEffort}
	payload, _ := json.Marshal(policy)
	request := loopbackRequest(http.MethodPut, "/api/routing/policy", string(payload))
	response := httptest.NewRecorder()
	s.handleRoutingPolicy(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	s.providerMu.Lock()
	handoff := s.providerHandoff
	s.providerMu.Unlock()
	if handoff == nil {
		t.Fatal("expected pending handoff")
	}
	active := s.activeProviderIdentity()
	if !sameProvider(handoff.Target, active) {
		t.Fatalf("handoff target=%#v active=%#v", handoff.Target, active)
	}
	if handoff.Target.Model != active.Model || normalizedProviderURL(handoff.Target.BaseURL) != normalizedProviderURL("https://model-specific.example/v1") {
		t.Fatalf("handoff target did not use hydrated route: %#v", handoff.Target)
	}
}

func TestRoutingPolicyCanLeaveOfficialModeConsistently(t *testing.T) {
	s := newRoutingTestServer(t)
	profilesList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.applyRoutingPolicyTransaction(profilesList, routing.RoutingPolicy{Official: true}); err != nil {
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
	identity := s.activeProviderIdentity()
	provider, _ := catalog.Provider(catalog.ModelRoutes[0].ProviderID)
	if identity.Official || identity.ID != provider.ID {
		t.Fatalf("identity = %#v, want provider %q", identity, provider.ID)
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

func TestRoutingPolicyRejectsCrossProviderSwitchWhileAgentBusy(t *testing.T) {
	s := newRoutingTestServer(t)
	agent := &sessionSwitchAgentFake{status: agentbridge.Status{Running: true, Busy: true, SessionID: "session-a"}}
	s.Agent = agent
	catalog, _, err := s.currentRouting()
	if err != nil {
		t.Fatal(err)
	}
	policy := catalog.Policy
	policy.Default = catalog.ModelRoutes[1].Name
	payload, _ := json.Marshal(policy)
	request := loopbackRequest(http.MethodPut, "/api/routing/policy", string(payload))
	response := httptest.NewRecorder()
	s.handleRoutingPolicy(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Policy.Default == policy.Default {
		t.Fatal("busy switch changed routing policy")
	}
}

func TestProfileActivateKeepsCombinedRoutingConfig(t *testing.T) {
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
	sw := &switcher.Switcher{ConfigPath: configPath, BackupsDir: filepath.Join(dir, "backups"), Profiles: profileStore}
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
