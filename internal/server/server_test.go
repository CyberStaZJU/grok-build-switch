package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"grok_switch/internal/remoteaccess"
	"grok_switch/internal/settings"
)

func loopbackRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestListenRejectsInvalidPreferredPort(t *testing.T) {
	server := &Server{}
	if _, _, err := server.Listen(70000); err == nil {
		t.Fatal("Listen() accepted an invalid preferred port")
	}
}

func TestOfficialAnthropicProfileMutationsLeaveRoutingAndConfigUnchanged(t *testing.T) {
	s := newRoutingTestServer(t)
	beforeProfiles, err := os.ReadFile(s.Profiles.Path())
	if err != nil {
		t.Fatal(err)
	}
	beforeRouting, err := os.ReadFile(s.Routing.Path())
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(s.Switcher.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Profiles.List()
	if err != nil || len(items) == 0 {
		t.Fatalf("profiles=%#v err=%v", items, err)
	}

	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
		invoke http.HandlerFunc
	}{
		{"create profile base", http.MethodPost, "/api/profiles", `{"name":"official","base_url":"https://api.anthropic.com/v1","default_model":"m","models":[{"name":"m","model":"m"}]}`, s.handleProfiles},
		{"create model override", http.MethodPost, "/api/profiles", `{"name":"official override","base_url":"https://messages.example/v1","default_model":"m","models":[{"name":"m","model":"m","base_url":"https://api.anthropic.com/v1"}]}`, s.handleProfiles},
		{"create unicode dot", http.MethodPost, "/api/profiles", `{"name":"official unicode","base_url":"https://api。anthropic.com/v1","default_model":"m","models":[{"name":"m","model":"m"}]}`, s.handleProfiles},
		{"create unicode override", http.MethodPost, "/api/profiles", `{"name":"official unicode override","base_url":"https://messages.example/v1","default_model":"m","models":[{"name":"m","model":"m","base_url":"https://api｡anthropic.com/v1"}]}`, s.handleProfiles},
		{"update profile base", http.MethodPut, "/api/profiles/" + items[0].ID, `{"name":"official","base_url":"https://api．anthropic.com/v1","default_model":"m","models":[{"name":"m","model":"m"}]}`, s.handleProfileByID},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.invoke(response, loopbackRequest(test.method, test.target, test.body))
			if response.Code < 400 || !strings.Contains(response.Body.String(), "不支持 Anthropic 官方 API 直连") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertFileBytesEqual(t, s.Profiles.Path(), beforeProfiles)
			assertFileBytesEqual(t, s.Routing.Path(), beforeRouting)
			assertFileBytesEqual(t, s.Switcher.ConfigPath, beforeConfig)
		})
	}
}

func TestConfigPreviewRejectsOfficialAnthropicEndpoint(t *testing.T) {
	s := newRoutingTestServer(t)
	beforeConfig, err := os.ReadFile(s.Switcher.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.handleConfigPreview(response, loopbackRequest(http.MethodPost, "/api/config/preview", `{"name":"official","base_url":"https://api.anthropic.com/v1","default_model":"m","models":[{"name":"m","model":"m"}]}`))
	if response.Code < 400 || !strings.Contains(response.Body.String(), "不支持 Anthropic 官方 API 直连") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	assertFileBytesEqual(t, s.Switcher.ConfigPath, beforeConfig)
}

func TestConfigEditorRejectsOfficialAnthropicEndpoint(t *testing.T) {
	s := newRoutingTestServer(t)
	beforeConfig, err := os.ReadFile(s.Switcher.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		"[endpoints]\nmodels_base_url = \"https://api.anthropic.com/v1\"\n[models]\ndefault = \"m\"\n[model.m]\nmodel = \"m\"\n",
		"[endpoints]\nmodels_base_url = \"https://messages.example/v1\"\n[models]\ndefault = \"m\"\n[model.m]\nmodel = \"m\"\nbase_url = \"https://api.anthropic.com/v1\"\n",
		"[endpoints]\nmodels_base_url = \"https://api。anthropic.com/v1\"\n[models]\ndefault = \"m\"\n[model.m]\nmodel = \"m\"\n",
		"[endpoints]\nmodels_base_url = \"https://messages.example/v1\"\n[models]\ndefault = \"m\"\n[model.m]\nmodel = \"m\"\nbase_url = \"https://api｡anthropic.com/v1\"\n",
	} {
		payload, err := json.Marshal(map[string]string{"content": content})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		s.handleConfig(response, loopbackRequest(http.MethodPut, "/api/config", string(payload)))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "不支持 Anthropic 官方 API 直连") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertFileBytesEqual(t, s.Switcher.ConfigPath, beforeConfig)
	}
}

func assertFileBytesEqual(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s changed\nwant=%s\ngot=%s", path, want, got)
	}
}

func TestRemovedRoutesReturnNotFound(t *testing.T) {
	s := &Server{}
	mux := http.NewServeMux()
	s.routes(mux)
	for _, route := range []string{
		"/api/agent/status",
		"/api/agent/sessions",
		"/api/agent/ws",
		"/api/session-graph",
		"/api/session-graph/merge",
		"/api/backups",
		"/api/backups/config.toml/restore",
		"/api/grok-auth",
		"/api/grok-auth/refresh",
		"/api/grok-pool",
		"/api/grok-pool/inspect",
		"/api/grok-pool/bulk",
		"/api/grok-pool/import-dir",
		"/api/grok-pool/open-auth-dir",
		"/api/grok-pool/accounts/account-id",
		"/api/cpa-mint",
		"/api/registrar",
		"/api/registrar/probe",
		"/api/registrar/start",
		"/api/registrar/stop",
		"/api/registrar/job",
		"/api/registrar/job/log",
		"/api/codebuddy/status",
		"/codebuddy/v1",
		"/codebuddy/v1/models",
		"/codebuddy/v1/chat/completions",
		"/grok/v1",
		"/grok/v1/responses",
	} {
		request := httptest.NewRequest(http.MethodGet, route, nil)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", route, response.Code)
		}
	}
}

func TestRemoteRequestsAreRejectedByDefault(t *testing.T) {
	store := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	remote := remoteaccess.NewStore(filepath.Join(t.TempDir(), "remote_access.json"))
	s := &Server{Settings: store, RemoteAccess: remote}
	next := s.withAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:17878/api/profiles", nil)
	req.RemoteAddr = "192.168.1.10:40000"
	response := httptest.NewRecorder()
	next.ServeHTTP(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestRemoteRequestWithoutSessionPromptsPairing(t *testing.T) {
	settingsStore := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	current := settings.Default()
	current.LANAccessEnabled = true
	if _, err := settingsStore.Update(current); err != nil {
		t.Fatal(err)
	}
	remote := remoteaccess.NewStore(filepath.Join(t.TempDir(), "remote_access.json"))
	s := &Server{Settings: settingsStore, RemoteAccess: remote}
	next := s.withAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Run("browser page redirects to pairing", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:17878/", nil)
		req.RemoteAddr = "192.168.1.20:40000"
		response := httptest.NewRecorder()
		next.ServeHTTP(response, req)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusSeeOther, response.Body.String())
		}
		if location := response.Header().Get("Location"); location != "/pair" {
			t.Fatalf("Location = %q, want /pair", location)
		}
	})

	t.Run("API returns friendly unauthorized response", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:17878/api/status", nil)
		req.RemoteAddr = "192.168.1.20:40001"
		response := httptest.NewRecorder()
		next.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusUnauthorized, response.Body.String())
		}
		body := response.Body.String()
		if !strings.Contains(body, "请先使用电脑端生成的二维码完成配对") {
			t.Fatalf("unexpected body: %s", body)
		}
		if strings.Contains(body, "named cookie not present") {
			t.Fatalf("raw missing-cookie error leaked: %s", body)
		}
	})
}

func TestRemoteSessionAndOriginProtection(t *testing.T) {
	settingsStore := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	current := settings.Default()
	current.LANAccessEnabled = true
	if _, err := settingsStore.Update(current); err != nil {
		t.Fatal(err)
	}
	remote := remoteaccess.NewStore(filepath.Join(t.TempDir(), "remote_access.json"))
	snapshot, err := remote.Get()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Settings: settingsStore, RemoteAccess: remote}
	next := s.withAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	valid := httptest.NewRequest(http.MethodPost, "http://192.168.1.10:17878/api/lan-access", strings.NewReader("{}"))
	valid.RemoteAddr = "192.168.1.10:40000"
	valid.Host = "192.168.1.10:17878"
	valid.Header.Set("Origin", "http://192.168.1.10:17878")
	csrfToken, err := s.csrfToken()
	if err != nil {
		t.Fatal(err)
	}
	valid.Header.Set(csrfHeader, csrfToken)
	valid.AddCookie(&http.Cookie{Name: lanSessionCookie, Value: snapshot.SessionToken})
	response := httptest.NewRecorder()
	next.ServeHTTP(response, valid)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid session status = %d, want %d", response.Code, http.StatusNoContent)
	}

	forged := valid.Clone(valid.Context())
	forged.Header.Set("Origin", "http://attacker.example")
	response = httptest.NewRecorder()
	next.ServeHTTP(response, forged)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forged origin status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestPairingSetsHTTPOnlySessionCookie(t *testing.T) {
	settingsStore := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	current := settings.Default()
	current.LANAccessEnabled = true
	if _, err := settingsStore.Update(current); err != nil {
		t.Fatal(err)
	}
	remote := remoteaccess.NewStore(filepath.Join(t.TempDir(), "remote_access.json"))
	pairing, err := remote.NewPairing()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Settings: settingsStore, RemoteAccess: remote}
	req := httptest.NewRequest(http.MethodGet, "/pair?code="+pairing.PairingCode, nil)
	req.RemoteAddr = "192.168.1.10:40000"
	response := httptest.NewRecorder()
	s.handlePair(response, req)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != lanSessionCookie || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected session cookies: %#v", cookies)
	}
}

func TestReconfigureLANListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	s := &Server{
		ActualPort: port,
		listener:   listener,
		bindHost:   "127.0.0.1",
		httpServer: &http.Server{Handler: http.NotFoundHandler()},
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
	}()
	if err := s.reconfigureLANAccess(true); err != nil {
		t.Fatal(err)
	}
	if s.bindHost != "0.0.0.0" {
		t.Fatalf("bind host = %q, want 0.0.0.0", s.bindHost)
	}
	if err := s.reconfigureLANAccess(false); err != nil {
		t.Fatal(err)
	}
	if s.bindHost != "127.0.0.1" {
		t.Fatalf("bind host = %q, want 127.0.0.1", s.bindHost)
	}
}
