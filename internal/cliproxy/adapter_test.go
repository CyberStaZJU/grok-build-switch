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

func TestModelsSyncProviderQualifiedAliases(t *testing.T) {
	modelsCalls := 0
	var put oauthModelAliases
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			modelsCalls++
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{
				{"id": "shared", "owned_by": "openai"},
				{"id": "shared", "owned_by": "antigravity"},
				{"id": "grok-4", "owned_by": "xai"},
			}})
		case "/v0/management/oauth-model-alias":
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode(oauthModelAliasesResponse{Aliases: oauthModelAliases{
					"codex": {{Name: "custom", Alias: "custom/codex", Fork: true}, {Name: "old", Alias: "subscription/codex/old", Fork: true}},
					"kimi":  {{Name: "moonshot-v1", Alias: "user/kimi", Fork: true}},
				}})
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&put); err != nil {
				t.Fatal(err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	got, err := m.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if modelsCalls != 2 || len(got) != 3 {
		t.Fatalf("模型 bootstrap/去重错误: calls=%d models=%+v", modelsCalls, got)
	}
	want := map[string]string{
		"subscription/codex/shared":  "codex",
		"subscription/gemini/shared": "gemini",
		"subscription/grok/grok-4":   "grok",
	}
	for _, model := range got {
		if want[model.ID] != model.Provider || model.Label == "" {
			t.Errorf("模型 alias 错误: %+v", model)
		}
		delete(want, model.ID)
	}
	if len(want) != 0 {
		t.Fatalf("缺少 alias: %+v", want)
	}
	if len(put["codex"]) != 2 || put["codex"][0].Alias != "custom/codex" || put["codex"][1].Alias != "subscription/codex/shared" {
		t.Fatalf("未正确保留非本应用 alias 或替换旧 alias: %+v", put["codex"])
	}
	if len(put["antigravity"]) != 1 || put["antigravity"][0].Alias != "subscription/gemini/shared" || !put["antigravity"][0].Fork {
		t.Fatalf("antigravity channel alias 错误: %+v", put["antigravity"])
	}
	if len(put["xai"]) != 1 || put["xai"][0].Alias != "subscription/grok/grok-4" {
		t.Fatalf("xai channel alias 错误: %+v", put["xai"])
	}
	if len(put["kimi"]) != 1 || put["kimi"][0].Alias != "user/kimi" {
		t.Fatalf("未知/用户 channel alias 未保留: %+v", put["kimi"])
	}
	if _, exists := put["oauth-model-alias"]; exists {
		t.Fatalf("PUT 包含 GET wrapper 伪 channel: %+v", put)
	}
	persisted := loadModelAliases(m.Paths)
	if len(persisted["kimi"]) != 1 || persisted["kimi"][0].Alias != "user/kimi" {
		t.Fatalf("合并后的未知 channel 未持久化: %+v", persisted)
	}
}

func TestModelsFallbackWhenRefreshFails(t *testing.T) {
	modelsCalls := 0
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			modelsCalls++
			if modelsCalls > 1 {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-5", "owned_by": "codex"}}})
			return
		}
		if r.URL.Path == "/v0/management/oauth-model-alias" {
			if r.Method == http.MethodGet {
				json.NewEncoder(w).Encode(oauthModelAliasesResponse{Aliases: nil})
			}
			return
		}
		http.NotFound(w, r)
	}))
	got, err := m.Models(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "subscription/codex/gpt-5" {
		t.Fatalf("刷新失败时未稳健返回: %+v %v", got, err)
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
