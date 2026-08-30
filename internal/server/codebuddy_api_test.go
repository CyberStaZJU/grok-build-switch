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

func newCodeBuddyAPITestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	// Isolate optional filesystem key fallback (~/.grok/codebuddy-bridge/.env).
	t.Setenv("HOME", dir)
	t.Setenv("CODEBUDDY_API_KEY", "")
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
	return &Server{
		ActualPort: 19093,
		Paths:      paths.Paths{GrokConfig: configPath, DataDir: dir},
		Profiles:   profileStore,
		Routing:    routingStore,
		Switcher:   sw,
	}
}

func TestCodeBuddyAPIStatusAndSave(t *testing.T) {
	s := newCodeBuddyAPITestServer(t)
	// Seed CSRF so PUT is allowed without Origin.
	if _, err := s.csrfToken(); err != nil {
		t.Fatal(err)
	}

	// GET empty status
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/codebuddy", nil)
	req.RemoteAddr = "127.0.0.1:1"
	s.handleCodeBuddyAPI(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status GET code=%d body=%s", rr.Code, rr.Body.String())
	}
	var status codeBuddyStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Configured || status.HasAPIKey {
		t.Fatalf("expected empty status, got %+v", status)
	}
	if !strings.Contains(status.ProxyBaseURL, "/codebuddy-proxy/v1") {
		t.Fatalf("proxy url = %q", status.ProxyBaseURL)
	}

	// PUT save without activate
	body := `{"api_key":"ck_test_gui_key","default_model":"hy3","activate":false}`
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/codebuddy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, s.csrfSecret)
	req.RemoteAddr = "127.0.0.1:1"
	// CSRF is enforced by withAccess middleware; call handler directly is fine.
	s.handleCodeBuddyAPI(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save PUT code=%d body=%s", rr.Code, rr.Body.String())
	}
	var saveResp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &saveResp); err != nil {
		t.Fatal(err)
	}
	if saveResp["ok"] != true {
		t.Fatalf("save resp = %#v", saveResp)
	}

	list, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range list {
		if p.Source == codebuddy.SourceTag {
			found = true
			if p.DefaultModel != "hy3" {
				t.Fatalf("default_model=%q", p.DefaultModel)
			}
			if p.APIKey != "ck_test_gui_key" {
				t.Fatalf("api key not stored")
			}
		}
	}
	if !found {
		t.Fatal("codebuddy profile missing")
	}

	// Activate with different default
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/codebuddy/activate", strings.NewReader(`{"default_model":"glm-5.2"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:1"
	s.handleCodeBuddyActivate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("activate code=%d body=%s", rr.Code, rr.Body.String())
	}
	snap, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var profileID string
	for _, p := range list {
		if p.Source == codebuddy.SourceTag {
			profileID = p.ID
		}
	}
	// Re-list after activate may keep same id
	list, _ = s.Profiles.List()
	for _, p := range list {
		if p.Source == codebuddy.SourceTag {
			profileID = p.ID
		}
	}
	if snap.ActiveProviderID != profileID {
		t.Fatalf("active=%q want %q", snap.ActiveProviderID, profileID)
	}
	if policy := snap.ProviderPolicies[profileID]; policy.Default != profileID+":glm-5.2" {
		t.Fatalf("policy default=%q", policy.Default)
	}
}

func TestCodeBuddyModelsAPIUsesWorkBuddyCatalog(t *testing.T) {
	s := newCodeBuddyAPITestServer(t)
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".workbuddy", "local_storage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entry_test.info"), []byte(`[{"ts":2000,"data":{"agents":[{"name":"cli","models":["kimi-k3-2","glm-5.3-flash"]}],"models":[{"id":"kimi-k3-2","name":"Kimi-K3","maxInputTokens":1000000,"maxOutputTokens":32000,"supportsReasoning":true,"supportsToolCall":true,"reasoning":{"supportedEfforts":["low","high","xhigh"]}},{"id":"glm-5.3-flash","name":"GLM-5.3-Flash","maxInputTokens":1000000,"maxOutputTokens":32000,"supportsReasoning":true,"supportsToolCall":true,"reasoning":{"supportedEfforts":["low","high","max"]}}]}}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/codebuddy/models", nil)
	req.RemoteAddr = "127.0.0.1:1"
	s.handleCodeBuddyModels(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("models code=%d body=%s", rr.Code, rr.Body.String())
	}
	var response struct {
		Source            string                   `json:"source"`
		InferenceEndpoint string                   `json:"inference_endpoint"`
		Models            []codebuddy.CatalogModel `json:"models"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Source != "workbuddy_local_catalog" || response.InferenceEndpoint != codebuddy.DefaultUpstream || len(response.Models) != 2 {
		t.Fatalf("models response = %#v", response)
	}
}

func TestCodeBuddySyncCatalogUpdatesManagedProfile(t *testing.T) {
	s := newCodeBuddyAPITestServer(t)
	first, err := s.EnsureCodeBuddyProviderOpts(CodeBuddyEnsureOptions{APIKey: "ck_sync", DefaultModel: "hy3"})
	if err != nil {
		t.Fatal(err)
	}
	trimmed := first
	trimmed.Models = trimmed.Models[:1]
	trimmed.AvailableModels = []string{trimmed.Models[0].Name}
	trimmed.DefaultModel = trimmed.Models[0].Name
	if _, err := s.Profiles.Update(first.ID, trimmed); err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("HOME")
	dir := filepath.Join(home, ".workbuddy", "local_storage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entry_sync.info"), []byte(`[{"ts":3000,"data":{"agents":[{"name":"cli","models":["kimi-k3-2","glm-5.3"]}],"models":[{"id":"kimi-k3-2","name":"Kimi-K3","maxInputTokens":1000000,"maxOutputTokens":32000,"supportsReasoning":true,"supportsToolCall":true},{"id":"glm-5.3","name":"GLM-5.3","maxInputTokens":1000000,"maxOutputTokens":48000,"supportsReasoning":true,"supportsToolCall":true}]}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, err := s.EnsureCodeBuddyProviderOpts(CodeBuddyEnsureOptions{APIKey: "ck_sync", DefaultModel: "kimi-k3-2", SyncCatalog: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != first.ID || updated.DefaultModel != "kimi-k3-2" || len(updated.Models) != 2 {
		t.Fatalf("updated = %#v", updated)
	}
	if updated.Models[0].ContextWindow != 1000000 || updated.Models[1].MaxCompletionTokens != 48000 {
		t.Fatalf("catalog metadata not applied: %#v", updated.Models)
	}
}

func TestCodeBuddyProxyRejectsProfileModelOutsideTrustedCatalog(t *testing.T) {
	s := newCodeBuddyAPITestServer(t)
	profile, err := s.EnsureCodeBuddyProvider("ck_catalog", false)
	if err != nil {
		t.Fatal(err)
	}
	injected := profile
	injected.Models = append(injected.Models, profiles.ModelDef{Name: "removed-model", Model: "removed-model", BaseURL: profile.BaseURL, APIKey: profile.APIKey, APIBackend: "chat_completions"})
	injected.AvailableModels = append(injected.AvailableModels, "removed-model")
	if _, err := s.Profiles.Update(profile.ID, injected); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/codebuddy-proxy/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:1"
	response := httptest.NewRecorder()
	s.handleCodeBuddyProxy(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "removed-model") {
		t.Fatalf("untrusted profile model advertised: %s", response.Body.String())
	}
}

func TestManagedCodeBuddyProfileAllowsContextWindowOnly(t *testing.T) {
	s := newCodeBuddyAPITestServer(t)
	profile, err := s.EnsureCodeBuddyProvider("ck_context", false)
	if err != nil {
		t.Fatal(err)
	}
	mutation := profileMutationFromProfile(profile)
	mutation.Models[0].ContextWindow++
	payload, err := json.Marshal(mutation)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.handleProfileByID(response, loopbackRequest(http.MethodPut, "/api/profiles/"+profile.ID, string(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	updated, err := s.Profiles.Get(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Models[0].ContextWindow != profile.Models[0].ContextWindow+1 || updated.Source != codebuddy.SourceTag {
		t.Fatalf("updated = %#v", updated)
	}
}

func TestMaskSecret(t *testing.T) {
	if got := maskSecret("ck_abcdefghij"); !strings.Contains(got, "…") {
		t.Fatalf("got %q", got)
	}
	if got := maskSecret("short"); got != "********" {
		t.Fatalf("got %q", got)
	}
}
