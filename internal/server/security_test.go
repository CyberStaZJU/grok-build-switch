package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
