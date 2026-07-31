package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/remoteaccess"
	"grok_switch/internal/settings"
	"grok_switch/internal/ssh"
)

func TestRoutingPolicyStrictJSON(t *testing.T) {
	for _, payload := range []string{
		`{"defualt":"route"}`,
		`{"official":"true"}`,
		`{"subagents":{"explroe":"route"}}`,
		`{"subagents":{"explore":false}}`,
	} {
		t.Run(payload, func(t *testing.T) {
			s := newRoutingTestServer(t)
			req := loopbackRequest(http.MethodPut, "/api/routing/policy", payload)
			res := httptest.NewRecorder()
			s.handleRoutingPolicy(res, req)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", res.Code, res.Body.String())
			}
		})
	}
}

func TestOfficialLoggedInValidatesAuthJSON(t *testing.T) {
	s := newRoutingTestServer(t)
	if err := os.MkdirAll(s.Paths.GrokHome, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Paths.GrokHome, "auth.json")
	for _, invalid := range []string{"", `{`, `{}`, `{"access_token":""}`} {
		if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if s.officialLoggedIn() {
			t.Fatalf("officialLoggedIn accepted %q", invalid)
		}
	}
	for _, valid := range []string{
		`{"type":"xai","access_token":"token"}`,
		`{"https://auth.x.ai::123e4567-e89b-12d3-a456-426614174000":{"key":"token","refresh_token":"refresh","expires_at":"2099-01-01T00:00:00Z","oidc_issuer":"https://auth.x.ai","oidc_client_id":"client"}}`,
	} {
		if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
			t.Fatal(err)
		}
		if !s.officialLoggedIn() {
			t.Fatalf("officialLoggedIn rejected valid credential %s", valid)
		}
	}
}

func TestCSRFEndpointDoesNotEnableCORS(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/csrf", nil)
	req.Header.Set("Origin", "http://attacker.example")
	res := httptest.NewRecorder()
	s.handleCSRF(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if origin := res.Header().Get("Access-Control-Allow-Origin"); origin != "" {
		t.Fatalf("unexpected CORS header %q", origin)
	}
}

func TestLANAccessMatrixRedactsProfilesAndBlocksSensitiveRoutes(t *testing.T) {
	s := newRoutingTestServer(t)
	settingsStore := settings.NewStore(filepath.Join(s.Paths.DataDir, "settings.json"))
	current := settings.Default()
	current.LANAccessEnabled = true
	if _, err := settingsStore.Update(current); err != nil {
		t.Fatal(err)
	}
	remoteStore := remoteaccess.NewStore(filepath.Join(s.Paths.DataDir, "remote-access.json"))
	snapshot, err := remoteStore.Get()
	if err != nil {
		t.Fatal(err)
	}
	s.Settings = settingsStore
	s.RemoteAccess = remoteStore
	s.SSH = ssh.NewHandler(filepath.Join(s.Paths.DataDir, "ssh"))
	mux := http.NewServeMux()
	s.routes(mux)
	handler := s.withAccess(mux)

	request := func(remoteAddr, method, target, body string, paired bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://192.168.1.10:17878"+target, strings.NewReader(body))
		req.RemoteAddr = remoteAddr
		req.Host = "192.168.1.10:17878"
		if method != http.MethodGet && method != http.MethodHead {
			req.Header.Set("Origin", "http://192.168.1.10:17878")
		}
		if paired {
			req.AddCookie(&http.Cookie{Name: lanSessionCookie, Value: snapshot.SessionToken})
		}
		if strings.HasPrefix(remoteAddr, "127.") && method != http.MethodGet && method != http.MethodHead {
			token, tokenErr := s.csrfToken()
			if tokenErr != nil {
				t.Fatal(tokenErr)
			}
			req.Header.Set(csrfHeader, token)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}

	unpaired := request("192.168.1.20:40000", http.MethodGet, "/api/profiles", "", false)
	if unpaired.Code != http.StatusUnauthorized {
		t.Fatalf("unpaired profiles status=%d body=%s", unpaired.Code, unpaired.Body.String())
	}

	paired := request("192.168.1.20:40001", http.MethodGet, "/api/profiles", "", true)
	if paired.Code != http.StatusOK {
		t.Fatalf("paired profiles status=%d body=%s", paired.Code, paired.Body.String())
	}
	pairedBody := paired.Body.String()
	for _, secret := range []string{"provider-secret-one", "provider-secret-two", "model-secret-two", "header-secret", "X-Secret", "private-one.example", "private-two.example"} {
		if strings.Contains(pairedBody, secret) {
			t.Fatalf("paired profile response leaked %q: %s", secret, pairedBody)
		}
	}
	if !strings.Contains(pairedBody, `"has_api_key":true`) {
		t.Fatalf("paired profile response omitted has_api_key: %s", pairedBody)
	}

	loopback := request("127.0.0.1:40002", http.MethodGet, "/api/profiles", "", false)
	if loopback.Code != http.StatusOK || !strings.Contains(loopback.Body.String(), "provider-secret-one") || !strings.Contains(loopback.Body.String(), "header-secret") {
		t.Fatalf("loopback profile management data unavailable: status=%d body=%s", loopback.Code, loopback.Body.String())
	}

	for _, target := range []string{"/api/config", "/api/ssh/connections", "/api/subscription-proxy"} {
		res := request("192.168.1.20:40003", http.MethodGet, target, "", true)
		if res.Code != http.StatusForbidden {
			t.Fatalf("paired %s status=%d want 403 body=%s", target, res.Code, res.Body.String())
		}
	}
	config := request("127.0.0.1:40004", http.MethodGet, "/api/config", "", false)
	if config.Code != http.StatusOK || !strings.Contains(config.Body.String(), `"content"`) {
		t.Fatalf("loopback config status=%d body=%s", config.Code, config.Body.String())
	}
	sshList := request("127.0.0.1:40005", http.MethodGet, "/api/ssh/connections", "", false)
	if sshList.Code != http.StatusOK {
		t.Fatalf("loopback ssh status=%d body=%s", sshList.Code, sshList.Body.String())
	}
}

func TestPairedLANSubscriptionProxyManagementIsBlockedBeforeSensitiveFacade(t *testing.T) {
	settingsStore := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	current := settings.Default()
	current.LANAccessEnabled = true
	if _, err := settingsStore.Update(current); err != nil {
		t.Fatal(err)
	}
	remoteStore := remoteaccess.NewStore(filepath.Join(t.TempDir(), "remote.json"))
	snapshot, err := remoteStore.Get()
	if err != nil {
		t.Fatal(err)
	}
	proxy := &serviceActionProxy{status: SubscriptionProxyStatus{
		Installed:  true,
		Running:    true,
		Healthy:    true,
		State:      "running",
		ConfigPath: "/sensitive/cliproxy/config.yaml",
		BaseURL:    "http://127.0.0.1:9999",
	}}
	s := &Server{Settings: settingsStore, RemoteAccess: remoteStore, SubscriptionProxy: proxy}
	mux := http.NewServeMux()
	s.routes(mux)
	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:17878/api/subscription-proxy", nil)
	req.RemoteAddr = "192.168.1.20:42000"
	req.Host = "192.168.1.10:17878"
	req.AddCookie(&http.Cookie{Name: lanSessionCookie, Value: snapshot.SessionToken})
	res := httptest.NewRecorder()
	s.withAccess(mux).ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("paired subscription status=%d want 403 body=%s", res.Code, res.Body.String())
	}
	for _, secret := range []string{"/sensitive/cliproxy/config.yaml", "127.0.0.1:9999"} {
		if strings.Contains(res.Body.String(), secret) {
			t.Fatalf("paired subscription response leaked %q: %s", secret, res.Body.String())
		}
	}
	if proxy.accountsCalls != 0 || proxy.modelsCalls != 0 {
		t.Fatalf("sensitive subscription facade was called: accounts=%d models=%d", proxy.accountsCalls, proxy.modelsCalls)
	}

	loopback := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17878/api/subscription-proxy", nil)
	loopback.RemoteAddr = "127.0.0.1:42001"
	loopback.Host = "127.0.0.1:17878"
	loopbackRes := httptest.NewRecorder()
	s.withAccess(mux).ServeHTTP(loopbackRes, loopback)
	if loopbackRes.Code != http.StatusOK || !strings.Contains(loopbackRes.Body.String(), "/sensitive/cliproxy/config.yaml") {
		t.Fatalf("loopback subscription management unavailable: status=%d body=%s", loopbackRes.Code, loopbackRes.Body.String())
	}
	if proxy.accountsCalls != 1 || proxy.modelsCalls != 1 {
		t.Fatalf("loopback subscription facade calls: accounts=%d models=%d", proxy.accountsCalls, proxy.modelsCalls)
	}
}

func TestPairedLANMutationsAndCredentialProbesAreLoopbackOnly(t *testing.T) {
	settingsStore := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	current := settings.Default()
	current.LANAccessEnabled = true
	if _, err := settingsStore.Update(current); err != nil {
		t.Fatal(err)
	}
	remoteStore := remoteaccess.NewStore(filepath.Join(t.TempDir(), "remote.json"))
	snapshot, err := remoteStore.Get()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Settings: settingsStore, RemoteAccess: remoteStore}
	next := s.withAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct{ method, target string }{
		{http.MethodPost, "/api/profiles"},
		{http.MethodPut, "/api/profiles/id"},
		{http.MethodDelete, "/api/profiles/id"},
		{http.MethodPut, "/api/settings"},
		{http.MethodPost, "/api/official/activate"},
		{http.MethodPost, "/api/import"},
		{http.MethodPost, "/api/models/fetch"},
		{http.MethodPost, "/api/models/reasoning-efforts"},
		{http.MethodPost, "/api/connection/test"},
		{http.MethodGet, "/api/cache-stats"},
	} {
		req := httptest.NewRequest(tc.method, "http://192.168.1.10:17878"+tc.target, strings.NewReader(`{}`))
		req.RemoteAddr = "192.168.1.20:41000"
		req.Host = "192.168.1.10:17878"
		if tc.method != http.MethodGet {
			req.Header.Set("Origin", "http://192.168.1.10:17878")
		}
		req.AddCookie(&http.Cookie{Name: lanSessionCookie, Value: snapshot.SessionToken})
		res := httptest.NewRecorder()
		next.ServeHTTP(res, req)
		if res.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d want 403", tc.method, tc.target, res.Code)
		}
	}
}

func TestLoopbackWriteCSRFProtection(t *testing.T) {
	s := &Server{}
	next := s.withAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	token, err := s.csrfToken()
	if err != nil {
		t.Fatal(err)
	}
	request := func(origin, supplied string) int {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:17878/api/settings", strings.NewReader(`{}`))
		req.RemoteAddr = "127.0.0.1:40000"
		req.Host = "127.0.0.1:17878"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if supplied != "" {
			req.Header.Set(csrfHeader, supplied)
		}
		res := httptest.NewRecorder()
		next.ServeHTTP(res, req)
		return res.Code
	}
	if got := request("http://attacker.example", token); got != http.StatusForbidden {
		t.Fatalf("malicious origin status = %d", got)
	}
	if got := request("http://127.0.0.1:17878", ""); got != http.StatusForbidden {
		t.Fatalf("missing token status = %d", got)
	}
	if got := request("http://127.0.0.1:17878", token); got != http.StatusNoContent {
		t.Fatalf("valid browser status = %d", got)
	}
	if got := request("", token); got != http.StatusNoContent {
		t.Fatalf("native token status = %d", got)
	}
}
