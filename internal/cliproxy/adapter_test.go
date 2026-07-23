package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"grok_switch/internal/server"
)

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = r.target.Scheme
	clone.URL.Host = r.target.Host
	return r.base.RoundTrip(clone)
}

func testManager(t *testing.T, h http.Handler) (*Manager, fakeKeys) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	keys := fakeKeys{inferenceAccount: "infer-secret", managementAccount: "manage-secret"}
	m := NewManager(t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "missing"), keys)
	m.HTTPClient = &http.Client{Transport: rewriteTransport{u, http.DefaultTransport}}
	return m, keys
}

func TestManagerSeparatesManagementAndInferenceKeys(t *testing.T) {
	var management, inference string
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			management = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(map[string]any{"files": []any{}})
			return
		}
		inference = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	if _, err := m.Accounts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if management != "Bearer manage-secret" || inference != "Bearer infer-secret" {
		t.Fatalf("header 密钥未分离: %q %q", management, inference)
	}
}

func TestOAuthEndpointMapping(t *testing.T) {
	seen := map[string]bool{}
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		json.NewEncoder(w).Encode(map[string]string{"state": "s", "url": "https://example.com/login"})
	}))
	for _, p := range []string{"codex", "antigravity", "xai"} {
		if _, err := m.StartLogin(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"/v0/management/codex-auth-url", "/v0/management/antigravity-auth-url", "/v0/management/xai-auth-url"} {
		if !seen[p] {
			t.Errorf("未请求 %s", p)
		}
	}
}

func TestManagerErrorRedacted(t *testing.T) {
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "manage-secret token /private/path", 500) }))
	_, err := m.Accounts(context.Background())
	if err == nil || containsAny(err.Error(), "manage-secret", "token", "/private") {
		t.Fatalf("错误未脱敏: %v", err)
	}
}
func containsAny(s string, values ...string) bool {
	for _, v := range values {
		if len(v) > 0 && stringContains(s, v) {
			return true
		}
	}
	return false
}
func stringContains(s, v string) bool {
	return len(v) <= len(s) && func() bool {
		for i := 0; i+len(v) <= len(s); i++ {
			if s[i:i+len(v)] == v {
				return true
			}
		}
		return false
	}()
}

func TestUninstalledStatusDoesNotCreateFiles(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root, t.TempDir(), filepath.Join(root, "bundle"), fakeKeys{})
	st, err := m.Status(context.Background())
	if err != nil || st.Installed || st.Running {
		t.Fatalf("未安装状态错误: %+v %v", st, err)
	}
	if _, err := os.Stat(m.Paths.Root); !os.IsNotExist(err) {
		t.Fatal("GET status 不应创建目录")
	}
}

func TestSelectedModelsPersistenceAndMode(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir(), "", fakeKeys{})
	want := []server.SubscriptionProxyModel{{ID: "gpt-5", Provider: "codex", Label: "gpt-5"}}
	if err := m.SetSelectedModels(want); err != nil {
		t.Fatal(err)
	}
	got, err := m.SelectedModels()
	if err != nil || len(got) != 1 || got[0].ID != "gpt-5" {
		t.Fatalf("持久化失败: %+v %v", got, err)
	}
	info, err := os.Stat(filepath.Join(m.Paths.Root, "selected-models.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("权限错误: %v %v", info, err)
	}
}
