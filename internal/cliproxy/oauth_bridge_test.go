package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestOAuthCallbackEndpointsCoverCodexAndAntigravity(t *testing.T) {
	seen := map[string]string{}
	for _, endpoint := range oauthCallbackEndpoints {
		if endpoint.Port == "" || endpoint.Provider == "" {
			t.Fatalf("invalid endpoint: %+v", endpoint)
		}
		if prev, ok := seen[endpoint.Port]; ok {
			t.Fatalf("duplicate callback port %s (%s and %s)", endpoint.Port, prev, endpoint.Provider)
		}
		seen[endpoint.Port] = endpoint.Provider
	}
	if seen["1455"] != "codex" {
		t.Fatalf("codex callback port missing: %#v", seen)
	}
	if seen["51121"] != "antigravity" {
		t.Fatalf("antigravity callback port missing: %#v", seen)
	}
}

func TestOAuthBrowserCallbackForwardsAntigravityWithoutProviderQuery(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []map[string]string
	)
	mgmt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/oauth-callback" {
			http.NotFound(w, r)
			return
		}
		values := map[string]string{"method": r.Method}
		if r.Method == http.MethodGet {
			q := r.URL.Query()
			values["provider"] = q.Get("provider")
			values["state"] = q.Get("state")
			values["code"] = q.Get("code")
			mu.Lock()
			requests = append(requests, values)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		_ = json.Unmarshal(body, &values)
		mu.Lock()
		requests = append(requests, values)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer mgmt.Close()

	prev := oauthManagementBase
	oauthManagementBase = mgmt.URL
	defer func() { oauthManagementBase = prev }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth-callback?state=abc123&code=tok", nil)
	handleOAuthBrowserCallback(rec, req, oauthCallbackEndpoint{Port: "51121", Provider: "antigravity"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("management API received no oauth-callback")
	}
	got := requests[0]
	if got["provider"] != "antigravity" || got["state"] != "abc123" || got["code"] != "tok" {
		t.Fatalf("unexpected forward payload: %#v", got)
	}
}

func TestOAuthBrowserCallbackRequiresState(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth-callback?code=tok", nil)
	handleOAuthBrowserCallback(rec, req, oauthCallbackEndpoint{Port: "51121", Provider: "antigravity"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "回调缺少 state") {
		t.Fatalf("expected missing-state rejection, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestForwardOAuthCallbackFallsBackToPOST(t *testing.T) {
	var (
		mu    sync.Mutex
		posts []map[string]string
	)
	mgmt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, `{"status":"error"}`, http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		values := map[string]string{}
		_ = json.Unmarshal(body, &values)
		mu.Lock()
		posts = append(posts, values)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer mgmt.Close()

	prev := oauthManagementBase
	oauthManagementBase = mgmt.URL
	defer func() { oauthManagementBase = prev }()

	if err := forwardOAuthCallback(context.Background(), "antigravity", "state-1", "code-1", "", "", "http://localhost:51121/oauth-callback?state=state-1&code=code-1"); err != nil {
		t.Fatalf("forward failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(posts) != 1 {
		t.Fatalf("expected one POST, got %#v", posts)
	}
	got := posts[0]
	if got["provider"] != "antigravity" || got["state"] != "state-1" || got["code"] != "code-1" {
		t.Fatalf("unexpected POST payload: %#v", got)
	}
	if !strings.Contains(got["redirect_url"], "localhost:51121/oauth-callback") {
		t.Fatalf("redirect_url=%q", got["redirect_url"])
	}
}

func TestListenOAuthCallbackAllowsAddrInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := listenOAuthCallback(oauthCallbackEndpoint{Port: port, Provider: "codex"}); err != nil {
		t.Fatalf("addr-in-use should be ignored: %v", err)
	}
}

func TestLoginStatusMessageByProvider(t *testing.T) {
	if msg := loginStatusMessage("antigravity"); !strings.Contains(msg, "Google") {
		t.Fatalf("antigravity message=%q", msg)
	}
	if msg := loginStatusMessage("xai"); !strings.Contains(msg, "xAI") {
		t.Fatalf("xai message=%q", msg)
	}
	if msg := loginStatusMessage("codex"); !strings.Contains(msg, "ChatGPT") {
		t.Fatalf("codex message=%q", msg)
	}
}
