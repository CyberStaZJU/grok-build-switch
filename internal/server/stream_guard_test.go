package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
	"grok_switch/internal/switcher"
)

// guardTestServer builds a Server backed by a real profile store so the guard
// resolves its upstream the same way production does.
func guardTestServer(t *testing.T, profilesList []profiles.Profile) (*Server, *profiles.Store) {
	t.Helper()
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[telemetry]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, profile := range profilesList {
		if _, err := profileStore.Create(profile); err != nil {
			t.Fatal(err)
		}
	}
	routingStore := routing.NewStore(filepath.Join(dir, "routing.json"))
	if _, err := routingStore.Initialize(profileStore); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		ActualPort: 19099,
		Paths:      paths.Paths{GrokConfig: configPath, DataDir: dir},
		Profiles:   profileStore,
		Routing:    routingStore,
		Switcher:   &switcher.Switcher{ConfigPath: configPath, Profiles: profileStore},
	}
	return s, profileStore
}

func guardProfileFixture(baseURL string) profiles.Profile {
	return profiles.Profile{
		Name:           "API 池",
		UpstreamFormat: "openai_responses",
		BaseURL:        baseURL,
		APIKey:         "pool-key",
		DefaultModel:   "gpt-5.6-sol",
		Models: []profiles.ModelDef{
			{Name: "gpt-5.6-sol", Model: "gpt-5.6-sol", APIBackend: "responses"},
		},
	}
}

// responsesStreamWithKeepalive is a conforming Responses stream with the
// gateway heartbeat spliced in, which is what aborts the Grok turn.
func responsesStreamWithKeepalive() string {
	return "event: response.created\n" +
		`data: {"type":"response.created","sequence_number":0}` + "\n\n" +
		"event: keepalive\n" +
		`data: {"type":"keepalive"}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","sequence_number":1,"delta":"hi"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":2}` + "\n\n"
}

func TestStreamGuardStripsHeartbeatAndForwardsUpstream(t *testing.T) {
	var gotPath, gotAuth, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotModel, _ = payload["model"].(string)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(responsesStreamWithKeepalive()))
	}))
	defer upstream.Close()

	s, _ := guardTestServer(t, []profiles.Profile{guardProfileFixture(upstream.URL + "/v1")})

	req := httptest.NewRequest(http.MethodPost, StreamGuardPathPrefix+"/responses",
		strings.NewReader(`{"model":"gpt-5.6-sol","stream":true,"input":"hi"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handleStreamGuard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "keepalive") {
		t.Fatalf("heartbeat reached the client: %q", body)
	}
	if !strings.Contains(body, `"type":"response.output_text.delta"`) ||
		!strings.Contains(body, `"type":"response.completed"`) {
		t.Fatalf("real frames were dropped: %q", body)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotAuth != "Bearer pool-key" {
		t.Fatalf("upstream auth = %q", gotAuth)
	}
	if gotModel != "gpt-5.6-sol" {
		t.Fatalf("upstream model = %q", gotModel)
	}
}

func TestStreamGuardStripsHeartbeatOnChatCompletions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"type\":\"keepalive\"}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	s, _ := guardTestServer(t, []profiles.Profile{guardProfileFixture(upstream.URL + "/v1")})

	req := httptest.NewRequest(http.MethodPost, StreamGuardPathPrefix+"/chat/completions",
		strings.NewReader(`{"model":"gpt-5.6-sol","stream":true}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handleStreamGuard(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "keepalive") {
		t.Fatalf("heartbeat reached the client: %q", body)
	}
	if !strings.Contains(body, "chat.completion.chunk") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("chat chunks were dropped: %q", body)
	}
}

func TestStreamGuardRewritesModelToUpstreamID(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	profile := guardProfileFixture(upstream.URL + "/v1")
	profile.Models = []profiles.ModelDef{
		{Name: "local-alias", Model: "gpt-5.6-sol", APIBackend: "responses"},
	}
	profile.DefaultModel = "local-alias"
	s, _ := guardTestServer(t, []profiles.Profile{profile})

	req := httptest.NewRequest(http.MethodPost, StreamGuardPathPrefix+"/responses",
		strings.NewReader(`{"model":"local-alias","stream":false}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handleStreamGuard(rec, req)

	if gotModel != "gpt-5.6-sol" {
		t.Fatalf("upstream model = %q, want gpt-5.6-sol", gotModel)
	}
}

func TestStreamGuardRejectsNonLoopback(t *testing.T) {
	s, _ := guardTestServer(t, []profiles.Profile{guardProfileFixture("http://127.0.0.1:1/v1")})
	req := httptest.NewRequest(http.MethodPost, StreamGuardPathPrefix+"/responses", strings.NewReader("{}"))
	req.RemoteAddr = "10.1.2.3:5555"
	rec := httptest.NewRecorder()
	s.handleStreamGuard(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestStreamGuardPreservesUpstreamErrorAndStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"Service temporarily unavailable","type":"api_error"}}`))
	}))
	defer upstream.Close()

	s, _ := guardTestServer(t, []profiles.Profile{guardProfileFixture(upstream.URL + "/v1")})
	req := httptest.NewRequest(http.MethodPost, StreamGuardPathPrefix+"/responses", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handleStreamGuard(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Service temporarily unavailable") {
		t.Fatalf("upstream error body lost: %q", rec.Body.String())
	}
}

func TestStreamGuardServesLocalModelCatalog(t *testing.T) {
	s, _ := guardTestServer(t, []profiles.Profile{guardProfileFixture("http://127.0.0.1:1/v1")})
	req := httptest.NewRequest(http.MethodGet, StreamGuardPathPrefix+"/models", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handleStreamGuard(rec, req)

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != "gpt-5.6-sol" {
		t.Fatalf("catalog = %+v", payload.Data)
	}
}

func TestStreamGuardRejectsUpstreamPointingBackAtItself(t *testing.T) {
	s, store := guardTestServer(t, []profiles.Profile{guardProfileFixture("http://127.0.0.1:19099" + StreamGuardPathPrefix)})
	profile, err := store.List()
	if err != nil || len(profile) != 1 {
		t.Fatalf("list: %v", err)
	}
	// Deliberately clear the remembered upstream to model a broken config.
	item := profile[0]
	item.UpstreamBaseURL = ""
	if _, err := store.Update(item.ID, item); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, StreamGuardPathPrefix+"/responses", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handleStreamGuard(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "缺少原始 Base URL") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestSetStreamGuardEnabledRoundTripsBaseURL(t *testing.T) {
	const original = "http://api-pool.example.com:11303/v1"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()

	s, store := guardTestServer(t, []profiles.Profile{guardProfileFixture(original)})
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v", err)
	}
	id := list[0].ID

	enabled, err := s.SetStreamGuardEnabledOpt(id, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if enabled.BaseURL != s.StreamGuardBaseURL() {
		t.Fatalf("base_url = %q, want guard endpoint", enabled.BaseURL)
	}
	if enabled.UpstreamBaseURL != original {
		t.Fatalf("upstream_base_url = %q, want %q", enabled.UpstreamBaseURL, original)
	}
	for _, model := range enabled.Models {
		if model.BaseURL != s.StreamGuardBaseURL() {
			t.Fatalf("model base_url = %q, want guard endpoint", model.BaseURL)
		}
	}

	disabled, err := s.SetStreamGuardEnabledOpt(id, false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.BaseURL != original {
		t.Fatalf("base_url after disable = %q, want %q", disabled.BaseURL, original)
	}
	if disabled.UpstreamBaseURL != "" {
		t.Fatalf("upstream_base_url not cleared: %q", disabled.UpstreamBaseURL)
	}
}

func TestSetStreamGuardEnabledSurvivesProfileEdit(t *testing.T) {
	const original = "http://api-pool.example.com:11303/v1"
	s, store := guardTestServer(t, []profiles.Profile{guardProfileFixture(original)})
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v", err)
	}

	if _, err := s.SetStreamGuardEnabledOpt(list[0].ID, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	// Round-trip through the editor DTO the way the settings page does.
	current, err := store.Get(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	dto := profileMutationFromProfile(current)
	edited := dto.profile()
	edited.DefaultReasoningEffort = "high"
	if _, err := store.Update(list[0].ID, edited); err != nil {
		t.Fatalf("update: %v", err)
	}

	stored, err := store.Get(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UpstreamBaseURL != original {
		t.Fatalf("edit lost the upstream: %q", stored.UpstreamBaseURL)
	}
	if !isStreamGuardBaseURL(stored.BaseURL) {
		t.Fatalf("edit dropped the guard endpoint: %q", stored.BaseURL)
	}
}

// TestProfileEditKeepsGuardUpstream drives the real profile endpoint the
// settings page uses. The editor shows the guard endpoint as base_url and never
// sends upstream_base_url back, so an ordinary save used to strand the guard
// with no upstream: every forwarded request failed with 503 "缺少原始 Base URL".
func TestProfileEditKeepsGuardUpstream(t *testing.T) {
	const original = "http://api-pool.example.com:11303/v1"
	s, store := guardTestServer(t, []profiles.Profile{guardProfileFixture(original)})
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v", err)
	}
	id := list[0].ID

	if _, err := s.SetStreamGuardEnabledOpt(id, true); err != nil {
		t.Fatalf("enable: %v", err)
	}

	current, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	// The settings page builds its payload from the visible form: base_url is
	// the guard endpoint and no upstream_base_url is present.
	mutation := profileMutationFromProfile(current)
	mutation.UpstreamBaseURL = ""
	mutation.DefaultReasoningEffort = "high"
	payload, err := json.Marshal(mutation)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	s.handleProfileByID(recorder, loopbackRequest(http.MethodPut, "/api/profiles/"+id, string(payload)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	stored, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UpstreamBaseURL != original {
		t.Fatalf("edit lost the upstream: %q", stored.UpstreamBaseURL)
	}
	if !isStreamGuardBaseURL(stored.BaseURL) {
		t.Fatalf("edit dropped the guard endpoint: %q", stored.BaseURL)
	}
	if stored.DefaultReasoningEffort != "high" {
		t.Fatalf("edit not applied: effort = %q", stored.DefaultReasoningEffort)
	}

	// With the upstream preserved, forwarding resolves instead of failing.
	route, err := guardRouteFromProfile(stored)
	if err != nil {
		t.Fatalf("guard route after edit: %v", err)
	}
	if route.BaseURL != original {
		t.Fatalf("route base = %q, want %q", route.BaseURL, original)
	}
}

func TestStreamGuardStatusReportsEnabledProfiles(t *testing.T) {
	s, store := guardTestServer(t, []profiles.Profile{guardProfileFixture("http://gw.example/v1")})
	list, _ := store.List()
	if _, err := s.SetStreamGuardEnabledOpt(list[0].ID, true); err != nil {
		t.Fatalf("enable: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/stream-guard", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handleStreamGuardSettings(rec, req)

	var status streamGuardStatusDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if status.Endpoint != s.StreamGuardBaseURL() {
		t.Fatalf("endpoint = %q", status.Endpoint)
	}
	if len(status.Profiles) != 1 || !status.Profiles[0].Enabled {
		t.Fatalf("profiles = %+v", status.Profiles)
	}
	if status.Profiles[0].UpstreamBaseURL != "http://gw.example/v1" {
		t.Fatalf("upstream = %q", status.Profiles[0].UpstreamBaseURL)
	}
}

func TestStreamGuardSettingsRejectsNonLoopback(t *testing.T) {
	s, _ := guardTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/stream-guard", nil)
	req.RemoteAddr = "192.168.1.9:5555"
	rec := httptest.NewRecorder()
	s.handleStreamGuardSettings(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

// syncRecorder is a ResponseWriter that can be read while the handler is still
// writing, so a streaming test can observe frames before the stream ends.
type syncRecorder struct {
	mu      sync.Mutex
	header  http.Header
	status  int
	body    bytes.Buffer
	flushes int
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{header: http.Header{}}
}

func (s *syncRecorder) Header() http.Header { return s.header }

func (s *syncRecorder) WriteHeader(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == 0 {
		s.status = status
	}
}

func (s *syncRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body.Write(p)
}

func (s *syncRecorder) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushes++
}

func (s *syncRecorder) snapshot() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body.String(), s.status
}

// TestStreamGuardRouteRegistration locks the contract the client actually
// exercises: the guard prefix is the profile Base URL, so the request path is
// /stream-guard/v1/<api path> and the /v1 segment must not be duplicated when
// the request is forwarded upstream.
// TestStreamGuardHandlesRealCapturedFailureSequence replays the exact frames a
// live capture recorded from the API pool on 2026-09-19: a non-standard
// heartbeat, then a `response.failed` that omitted the schema-required `output`
// member while reporting a transient server_error. An unguarded client aborts
// on the heartbeat; a client that survives it hits the second defect. Both must
// be resolved, and the real upstream cause must reach the client so it retries.
func TestStreamGuardHandlesRealCapturedFailureSequence(t *testing.T) {
	keepalive := "event: keepalive\ndata: {\"type\":\"keepalive\",\"sequence_number\":2}\n\n"
	failed := "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{" +
		"\"id\":\"resp_0000000000000000000000000000000000000000000000000\",\"object\":\"response\"," +
		"\"created_at\":1789807241,\"status\":\"failed\",\"completed_at\":null," +
		"\"error\":{\"code\":\"server_error\",\"message\":\"Our servers are currently overloaded. Please try again later.\"}," +
		"\"model\":\"gpt-5.6-sol\",\"store\":false},\"sequence_number\":4}\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0}\n\n" + keepalive + failed))
	}))
	defer upstream.Close()

	s, _ := guardTestServer(t, []profiles.Profile{guardProfileFixture(upstream.URL + "/v1")})
	req := httptest.NewRequest(http.MethodPost, StreamGuardPathPrefix+"/responses",
		strings.NewReader(`{"model":"gpt-5.6-sol","stream":true}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handleStreamGuard(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "keepalive") {
		t.Fatalf("heartbeat reached the client: %q", body)
	}
	if !strings.Contains(body, "response.failed") {
		t.Fatalf("failure notification was dropped: %q", body)
	}
	if !strings.Contains(body, "overloaded") {
		t.Fatalf("real upstream cause was lost: %q", body)
	}
	if !strings.Contains(body, `"output"`) {
		t.Fatalf("required output member was not backfilled: %q", body)
	}
	if !strings.Contains(body, `"sequence_number":4`) {
		t.Fatalf("envelope sequence_number was dropped during repair: %q", body)
	}
}

func TestStreamGuardRouteRegistration(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	s, _ := guardTestServer(t, []profiles.Profile{guardProfileFixture(upstream.URL + "/v1")})

	mux := http.NewServeMux()
	s.routes(mux)

	req := httptest.NewRequest(http.MethodPost, s.StreamGuardBaseURL()+"/responses",
		strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("upstream path = %q, want /v1/responses", gotPath)
	}
}

// TestStreamGuardToggleOverHTTPDoesNotDeadlock drives the toggle through the
// registered HTTP route rather than calling the method directly.
//
// routingMu guards the routing transaction, and re-projecting after the profile
// change needs the already-locked variant. Calling the exported method from a
// handler that had taken the mutex deadlocked the server: the request hung and
// every later API call blocked behind it. Direct unit calls cannot catch that,
// so this goes through the mux.
func TestStreamGuardToggleOverHTTPDoesNotDeadlock(t *testing.T) {
	s, store := guardTestServer(t, []profiles.Profile{guardProfileFixture("http://gw.example/v1")})
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v", err)
	}
	id := list[0].ID

	mux := http.NewServeMux()
	s.routes(mux)

	body := fmt.Sprintf(`{"profile_id":%q,"enabled":true}`, id)
	req := httptest.NewRequest(http.MethodPut, "/api/stream-guard", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(rec, req)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("PUT /api/stream-guard deadlocked")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	stored, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !isStreamGuardBaseURL(stored.BaseURL) {
		t.Fatalf("guard not applied: base_url = %q", stored.BaseURL)
	}
	if stored.UpstreamBaseURL != "http://gw.example/v1" {
		t.Fatalf("upstream not remembered: %q", stored.UpstreamBaseURL)
	}

	// The server must still answer afterwards: a deadlock would have left the
	// mutex held and hung every later request.
	statusReq := httptest.NewRequest(http.MethodGet, "/api/stream-guard", nil)
	statusReq.RemoteAddr = "127.0.0.1:5555"
	statusRec := httptest.NewRecorder()
	statusDone := make(chan struct{})
	go func() {
		defer close(statusDone)
		mux.ServeHTTP(statusRec, statusReq)
	}()
	select {
	case <-statusDone:
	case <-time.After(10 * time.Second):
		t.Fatal("server unresponsive after toggle; mutex likely still held")
	}
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status after toggle = %d", statusRec.Code)
	}
}

func TestStreamGuardForwardsRemainingFramesBeforeUpstreamStalls(t *testing.T) {
	// The guard must not buffer the whole stream: frames already written must
	// reach the client while the upstream is still open.
	release := make(chan struct{})
	var closeOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer upstream.Close()
	defer closeOnce.Do(func() { close(release) })

	s, _ := guardTestServer(t, []profiles.Profile{guardProfileFixture(upstream.URL + "/v1")})

	req := httptest.NewRequest(http.MethodPost, StreamGuardPathPrefix+"/responses", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := newSyncRecorder()

	go func() { s.handleStreamGuard(rec, req) }()

	deadline := time.After(5 * time.Second)
	for {
		body, status := rec.snapshot()
		if strings.Contains(body, "response.created") {
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("first frame not flushed while upstream still open: %q", body)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}
