package cliproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/modelvariants"
	"grok_switch/internal/server"

	"gopkg.in/yaml.v3"
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
		switch r.URL.Path {
		case "/v0/management/auth-files":
			management = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(map[string]any{"files": []any{}})
		case "/v1/models":
			inference = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		default:
			http.NotFound(w, r)
		}
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

type configYAMLFixture struct {
	t          *testing.T
	yaml       []byte
	puts       int
	gets       int
	putHeaders http.Header
	rewritePUT func([]byte) []byte
	afterPUT   func(*configYAMLFixture)
	failPUT    bool
}

func (f *configYAMLFixture) serve(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/v0/management/config.yaml" {
		return false
	}
	if r.Header.Get("Authorization") != "Bearer manage-secret" {
		f.t.Errorf("management auth=%q", r.Header.Get("Authorization"))
	}
	switch r.Method {
	case http.MethodGet:
		f.gets++
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(f.yaml)
	case http.MethodPut:
		f.puts++
		f.putHeaders = r.Header.Clone()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			f.t.Fatal(err)
		}
		if f.failPUT {
			http.Error(w, "manage-secret private token", http.StatusUnprocessableEntity)
			return true
		}
		if f.rewritePUT != nil {
			raw = f.rewritePUT(raw)
		}
		f.yaml = raw
		if f.afterPUT != nil {
			f.afterPUT(f)
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "changed": []string{"config"}})
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
	return true
}

func TestReconcileFetchesCatalogOnlyAfterOperationLock(t *testing.T) {
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			t.Fatal("catalog fetch must not occur while another process lock is held")
		}
		http.NotFound(w, r)
	}))
	// testManager has its own data root; use a live-owner lock there.
	if err := m.Paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configOperationLockPath(m.Paths), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReconcileModels(context.Background()); err == nil {
		t.Fatal("reconcile should fail before catalog discovery when lock is held")
	}
}

func TestModelsReadOnlyDoesNotTouchManagementConfig(t *testing.T) {
	modelsCalls := 0
	fixture := &configYAMLFixture{t: t, yaml: []byte("host: 127.0.0.1\n")}
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		if r.URL.Path == "/v1/models" {
			modelsCalls++
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-5.6-terra", "owned_by": "openai"}}})
			return
		}
		http.NotFound(w, r)
	}))
	models, err := m.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "subscription/codex/gpt-5.6-terra" {
		t.Fatalf("Models() = %+v, %v", models, err)
	}
	if modelsCalls != 1 || fixture.gets != 0 || fixture.puts != 0 {
		t.Fatalf("read-only Models touched management config: models=%d gets=%d puts=%d", modelsCalls, fixture.gets, fixture.puts)
	}
}

func TestModelsSyncFullConfigPreservesUserStateAndShapesFast(t *testing.T) {
	modelsCalls := 0
	fixture := &configYAMLFixture{t: t, yaml: []byte(`# keep-top-comment
host: 127.0.0.1
custom-section:
  future: true
oauth-model-alias:
  codex:
    - name: custom
      alias: custom/codex
      fork: true
    - name: user-owned
      alias: subscription/codex/user-owned
      fork: true
  kimi:
    - name: moonshot-v1
      alias: user/kimi
      fork: true
payload:
  default:
    - models:
        - name: "*"
          protocol: codex
      params:
        temperature: 0
  override:
    - models:
        - name: user-fast-rule
          protocol: codex
      params:
        service_tier: priority
`)}
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			modelsCalls++
			data := []map[string]string{
				{"id": "gpt-5.6-terra", "owned_by": "openai"},
				{"id": "shared", "owned_by": "antigravity"},
				{"id": "grok-4", "owned_by": "xai"},
				// CLIProxy may expose generated aliases after the PUT. They must not
				// become physical models or produce -fast-fast recursion.
				{"id": "subscription/codex/gpt-5.6-terra-fast-fast", "owned_by": "openai"},
				{"id": "gpt-5.6-terra-fast", "owned_by": "openai"},
			}
			if fixture.puts > 0 {
				data = append(data,
					map[string]string{"id": "subscription/codex/gpt-5.6-terra", "owned_by": "openai"},
					map[string]string{"id": "subscription/codex/gpt-5.6-terra-fast", "owned_by": "openai"},
					map[string]string{"id": "subscription/gemini/shared", "owned_by": "antigravity"},
					map[string]string{"id": "subscription/grok/grok-4", "owned_by": "xai"},
				)
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		default:
			http.NotFound(w, r)
		}
	}))
	got, err := m.ReconcileModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if modelsCalls != 3 || fixture.puts != 1 || fixture.gets != 6 {
		t.Fatalf("unexpected calls: models=%d gets=%d puts=%d", modelsCalls, fixture.gets, fixture.puts)
	}
	if fixture.putHeaders.Get("Content-Type") != "application/yaml" {
		t.Fatalf("PUT content type=%q", fixture.putHeaders.Get("Content-Type"))
	}
	want := map[string]string{
		"subscription/codex/gpt-5.6-terra": "codex",
		"subscription/gemini/shared":       "gemini",
		"subscription/grok/grok-4":         "grok",
	}
	for _, model := range got {
		if want[model.ID] != model.Provider || model.Label == "" {
			t.Errorf("模型 alias 错误: %+v", model)
		}
		delete(want, model.ID)
	}
	if len(want) != 0 || len(got) != 3 {
		t.Fatalf("模型过滤错误: got=%+v missing=%v", got, want)
	}
	text := string(fixture.yaml)
	for _, preserved := range []string{"# keep-top-comment", "custom-section:", "subscription/codex/user-owned", "user/kimi", "temperature: 0", "user-fast-rule"} {
		if !strings.Contains(text, preserved) {
			t.Fatalf("full config lost %q:\n%s", preserved, text)
		}
	}
	standard, _ := modelvariants.CodexStandardAlias("gpt-5.6-terra")
	fast, _ := modelvariants.CodexFastAlias("gpt-5.6-terra")
	for _, created := range []string{standard, fast, "subscription/gemini/shared", "subscription/grok/grok-4", "service_tier: priority"} {
		if !strings.Contains(text, created) {
			t.Fatalf("full config missing %q:\n%s", created, text)
		}
	}
	if strings.Contains(text, "fast-fast") {
		t.Fatalf("recursive Fast alias generated:\n%s", text)
	}
	if strings.Count(text, "service_tier: priority") != 2 {
		t.Fatalf("user + managed priority rules were not both preserved:\n%s", text)
	}
	assertManagedConfigSemantics(t, fixture.yaml, generatedManagedConfig([]upstreamModel{
		{ID: "gpt-5.6-terra", OwnedBy: "openai"},
		{ID: "shared", OwnedBy: "antigravity"},
		{ID: "grok-4", OwnedBy: "xai"},
	}))
	ownership, exists, err := loadConfigOwnership(m.Paths)
	if err != nil || !exists || ownership.FastRuleFingerprint == "" || len(ownership.Aliases) != 4 {
		t.Fatalf("ownership ledger invalid: %+v exists=%v err=%v", ownership, exists, err)
	}
	if _, err := os.Stat(modelAliasesPath(m.Paths)); !os.IsNotExist(err) {
		t.Fatalf("new sync must not rewrite legacy sidecar: %v", err)
	}
}

func TestReconcileWaitsForObsoleteOwnedAliasesToDisappear(t *testing.T) {
	oldSource := []upstreamModel{{ID: "gpt-5.6-sol", OwnedBy: "codex"}}
	seed, oldOwnership, err := mergeManagedConfig(
		[]byte("host: 127.0.0.1\n"),
		generatedManagedConfig(oldSource),
		configOwnership{Version: configOwnershipVersion},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &configYAMLFixture{t: t, yaml: seed}
	modelsCalls := 0
	oldStandard, _ := modelvariants.CodexStandardAlias("gpt-5.6-sol")
	oldFast, _ := modelvariants.CodexFastAlias("gpt-5.6-sol")
	newStandard, _ := modelvariants.CodexStandardAlias("gpt-5.6-luna")
	newFast, _ := modelvariants.CodexFastAlias("gpt-5.6-luna")
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		modelsCalls++
		data := []map[string]string{{"id": "gpt-5.6-luna", "owned_by": "codex"}}
		if fixture.puts == 0 || modelsCalls <= 3 {
			data = append(data,
				map[string]string{"id": oldStandard, "owned_by": "codex"},
				map[string]string{"id": oldFast, "owned_by": "codex"},
			)
		}
		if fixture.puts > 0 {
			data = append(data,
				map[string]string{"id": newStandard, "owned_by": "codex"},
				map[string]string{"id": newFast, "owned_by": "codex"},
			)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	if err := m.Paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigOwnership(m.Paths, oldOwnership); err != nil {
		t.Fatal(err)
	}

	got, err := m.ReconcileModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if modelsCalls != 5 {
		t.Fatalf("reconcile returned before obsolete aliases disappeared stably: model calls=%d", modelsCalls)
	}
	if len(got) != 1 || got[0].ID != newStandard {
		t.Fatalf("unexpected reconciled catalog: %+v", got)
	}
}

func TestReconcileRemovesOwnedAliasesWhenTrustedCatalogBecomesEmpty(t *testing.T) {
	oldSource := []upstreamModel{{ID: "gpt-5.6-sol", OwnedBy: "codex"}}
	seed, oldOwnership, err := mergeManagedConfig(
		[]byte("host: 127.0.0.1\n"),
		generatedManagedConfig(oldSource),
		configOwnership{Version: configOwnershipVersion},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &configYAMLFixture{t: t, yaml: seed}
	modelsCalls := 0
	oldStandard, _ := modelvariants.CodexStandardAlias("gpt-5.6-sol")
	oldFast, _ := modelvariants.CodexFastAlias("gpt-5.6-sol")
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		modelsCalls++
		data := []map[string]string{}
		if fixture.puts == 0 || modelsCalls <= 2 {
			data = append(data,
				map[string]string{"id": oldStandard, "owned_by": "codex"},
				map[string]string{"id": oldFast, "owned_by": "codex"},
			)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	if err := m.Paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigOwnership(m.Paths, oldOwnership); err != nil {
		t.Fatal(err)
	}

	got, err := m.ReconcileModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || fixture.puts != 1 || modelsCalls != 4 {
		t.Fatalf("empty catalog cleanup did not converge: got=%+v puts=%d model calls=%d", got, fixture.puts, modelsCalls)
	}
	text := string(fixture.yaml)
	if strings.Contains(text, oldStandard) || strings.Contains(text, oldFast) || strings.Contains(text, "service_tier: priority") {
		t.Fatalf("obsolete managed config remained after empty catalog:\n%s", text)
	}
	ownership, exists, err := loadConfigOwnership(m.Paths)
	if err != nil || !exists || len(ownership.Aliases) != 0 || ownership.FastRuleFingerprint != "" {
		t.Fatalf("ownership not cleared: %+v exists=%v err=%v", ownership, exists, err)
	}
}

func TestSyncRebasesConcurrentConfigEditAndVerificationAcceptsCommentLoss(t *testing.T) {
	fixture := &configYAMLFixture{t: t, yaml: []byte("host: 127.0.0.1\n")}
	getCount := 0
	fixture.rewritePUT = func(raw []byte) []byte {
		var value any
		if err := yaml.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		plain, err := yaml.Marshal(value) // simulates CLIProxyAPI re-encoding and losing comments
		if err != nil {
			t.Fatal(err)
		}
		return plain
	}
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/config.yaml" && r.Method == http.MethodGet {
			getCount++
			if getCount == 2 {
				fixture.yaml = append(fixture.yaml, []byte("external-edit: preserved\n")...)
			}
		}
		if fixture.serve(w, r) {
			return
		}
		http.NotFound(w, r)
	}))
	models := []upstreamModel{{ID: "gpt-5.6-luna", OwnedBy: "codex"}}
	if _, err := m.syncModelAliases(context.Background(), models); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fixture.yaml), "external-edit: preserved") {
		t.Fatalf("second-GET rebase lost concurrent edit:\n%s", fixture.yaml)
	}
	if ownership, exists, err := loadConfigOwnership(m.Paths); err != nil || !exists || ownership.FastRuleFingerprint == "" {
		t.Fatalf("ledger not saved after semantic verification: %+v %v %v", ownership, exists, err)
	}
}

func TestSyncVerificationFailureLeavesOwnershipLedgerUnchanged(t *testing.T) {
	fixture := &configYAMLFixture{t: t, yaml: []byte("host: 127.0.0.1\n")}
	fixture.rewritePUT = func(raw []byte) []byte {
		return bytes.Replace(raw, []byte("service_tier: priority"), []byte("service_tier: standard"), 1)
	}
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		http.NotFound(w, r)
	}))
	before := configOwnership{Version: configOwnershipVersion, Aliases: []ownedAliasIdentity{{Channel: "codex", Name: "old", Alias: "subscription/codex/old"}}}
	if err := m.Paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigOwnership(m.Paths, before); err != nil {
		t.Fatal(err)
	}
	rawBefore, _ := os.ReadFile(configOwnershipPath(m.Paths))
	_, err := m.syncModelAliases(context.Background(), []upstreamModel{{ID: "gpt-5.6-sol", OwnedBy: "codex"}})
	if err == nil {
		t.Fatal("verification mismatch should fail")
	}
	rawAfter, _ := os.ReadFile(configOwnershipPath(m.Paths))
	if !bytes.Equal(rawBefore, rawAfter) {
		t.Fatalf("verification failure changed ledger:\nbefore=%s\nafter=%s", rawBefore, rawAfter)
	}
}

func TestSyncUnknownExternalPostPUTChangeFailsClosedAndKeepsJournal(t *testing.T) {
	fixture := &configYAMLFixture{t: t, yaml: []byte("host: 127.0.0.1\n")}
	fixture.afterPUT = func(f *configYAMLFixture) {
		f.yaml = append(f.yaml, []byte("external-user-edit: true\n")...)
	}
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		http.NotFound(w, r)
	}))
	if err := m.Paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	before := configOwnership{Version: configOwnershipVersion}
	if err := saveConfigOwnership(m.Paths, before); err != nil {
		t.Fatal(err)
	}
	beforeRaw, _ := os.ReadFile(configOwnershipPath(m.Paths))
	_, err := m.syncModelAliases(context.Background(), []upstreamModel{{ID: "gpt-5.6-sol", OwnedBy: "codex"}})
	if err == nil {
		t.Fatal("unknown post-PUT change must fail closed")
	}
	afterRaw, _ := os.ReadFile(configOwnershipPath(m.Paths))
	if !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatalf("unknown state changed ledger:\nbefore=%s\nafter=%s", beforeRaw, afterRaw)
	}
	if _, statErr := os.Stat(configTransactionPath(m.Paths)); statErr != nil {
		t.Fatalf("unresolved journal must remain: %v", statErr)
	}
	if !strings.Contains(string(fixture.yaml), "external-user-edit: true") {
		t.Fatalf("external edit was overwritten during recovery:\n%s", fixture.yaml)
	}
}

func TestSyncPUTFailureLeavesOwnershipLedgerUnchangedAndRedactsBody(t *testing.T) {
	fixture := &configYAMLFixture{t: t, yaml: []byte("host: 127.0.0.1\n"), failPUT: true}
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		http.NotFound(w, r)
	}))
	before := configOwnership{Version: configOwnershipVersion, Aliases: []ownedAliasIdentity{{Channel: "codex", Name: "old", Alias: "subscription/codex/old"}}}
	if err := m.Paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigOwnership(m.Paths, before); err != nil {
		t.Fatal(err)
	}
	rawBefore, _ := os.ReadFile(configOwnershipPath(m.Paths))
	_, err := m.syncModelAliases(context.Background(), []upstreamModel{{ID: "gpt-5.6-sol", OwnedBy: "codex"}})
	if err == nil || containsAny(err.Error(), "manage-secret", "private", "token") {
		t.Fatalf("PUT error missing/redaction failed: %v", err)
	}
	rawAfter, _ := os.ReadFile(configOwnershipPath(m.Paths))
	if !bytes.Equal(rawBefore, rawAfter) {
		t.Fatalf("failed PUT changed ledger:\nbefore=%s\nafter=%s", rawBefore, rawAfter)
	}
}

func TestModelsRefreshFailureFailsClosed(t *testing.T) {
	modelsCalls := 0
	fixture := &configYAMLFixture{t: t, yaml: []byte("host: 127.0.0.1\n")}
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		if r.URL.Path == "/v1/models" {
			modelsCalls++
			if modelsCalls > 1 {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-5", "owned_by": "codex"}}})
			return
		}
		http.NotFound(w, r)
	}))
	got, err := m.ReconcileModels(context.Background())
	if err == nil || len(got) != 0 {
		t.Fatalf("刷新失败必须 fail closed: %+v %v", got, err)
	}
}

func TestGeneratedManagedConfigAndSubscriptionModelsRequireExactProviderAttestation(t *testing.T) {
	rejectedOwners := []string{"not-openai", "openai-compatible", "mycodex", "codex-proxy", "google", "gemini", "not-xai", "grok", "xai-compatible", "vendor"}
	models := []upstreamModel{
		{ID: "gpt-openai-hint", OwnedBy: "vendor"},
		{ID: "codex-hint", OwnedBy: "vendor"},
		{ID: "gemini-hint", OwnedBy: "vendor"},
		{ID: "grok-hint", OwnedBy: "vendor"},
	}
	for _, owner := range rejectedOwners {
		models = append(models, upstreamModel{ID: "gpt-5.6-terra", OwnedBy: owner})
	}
	generated := generatedManagedConfig(models)
	if len(generated.Aliases) != 0 || len(generated.FastAliases) != 0 {
		t.Fatalf("untrusted providers generated managed aliases: %+v", generated)
	}
	if got := subscriptionModels(models); len(got) != 0 {
		t.Fatalf("untrusted providers entered subscription catalog: %+v", got)
	}

	accepted := []upstreamModel{
		{ID: "gpt-5.6-terra", OwnedBy: " OpenAI "},
		{ID: "custom", OwnedBy: "CODEX"},
		{ID: "gem", OwnedBy: "ANTIGRAVITY"},
		{ID: "grok-model", OwnedBy: "XAI"},
	}
	generated = generatedManagedConfig(accepted)
	standard, _ := modelvariants.CodexStandardAlias("gpt-5.6-terra")
	fast, _ := modelvariants.CodexFastAlias("gpt-5.6-terra")
	want := map[string]bool{
		"codex\x00" + standard:                   true,
		"codex\x00" + fast:                       true,
		"codex\x00subscription/codex/custom":     true,
		"antigravity\x00subscription/gemini/gem": true,
		"xai\x00subscription/grok/grok-model":    true,
	}
	for channel, aliases := range generated.Aliases {
		for _, alias := range aliases {
			delete(want, channel+"\x00"+alias.Alias)
		}
	}
	if len(want) != 0 || len(generated.FastAliases) != 1 || generated.FastAliases[0] != fast {
		t.Fatalf("exact attestation aliases missing=%v generated=%+v", want, generated)
	}
}

func TestAccountFilePathRejectsTraversalAndNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"", ".", "..", "nested/file.json", `nested\\file.json`, "/tmp/file.json", "bad\x00file.json"} {
		if _, err := accountFilePath(root, id); err == nil {
			t.Fatalf("accountFilePath(%q) unexpectedly succeeded", id)
		}
	}
	valid := "codex-user+tag@example.com.json"
	path, err := accountFilePath(root, valid)
	if err != nil || path != filepath.Join(root, valid) {
		t.Fatalf("accountFilePath valid = %q, %v", path, err)
	}
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := accountFilePath(root, "link.json"); err == nil {
		t.Fatal("symlink account unexpectedly accepted")
	}
}

func TestAccountsRejectUnsafeUpstreamID(t *testing.T) {
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{{"name": "..", "provider": "openai"}}})
	}))
	if _, err := m.Accounts(context.Background()); err == nil {
		t.Fatal("unsafe upstream account ID unexpectedly accepted")
	}
}

func TestAccountsDoNotInferProviderFromFilename(t *testing.T) {
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
			{"name": "openai-backup.json", "provider": "vendor", "status": "ready"},
			{"name": "codex.json", "provider": "openai", "status": "ready"},
		}})
	}))
	accounts, err := m.Accounts(context.Background())
	if err != nil || len(accounts) != 2 {
		t.Fatalf("Accounts() = %+v, %v", accounts, err)
	}
	if accounts[0].Provider != "" || accounts[1].Provider != "codex" {
		t.Fatalf("provider classification used filename/fuzzy inference: %+v", accounts)
	}
}

func TestGeneratedManagedConfigExactFastRegistryAndNoRecursion(t *testing.T) {
	generated := generatedManagedConfig([]upstreamModel{
		{ID: "", OwnedBy: "codex"},
		{ID: "gpt-5.6-terra", OwnedBy: "openai"},
		{ID: "gpt-5.6-terra", OwnedBy: "codex"},
		{ID: "gpt-5.6-terra-fast", OwnedBy: "codex"},
		{ID: "gpt-5.6-terra-fast-fast", OwnedBy: "codex"},
		{ID: "custom-fast", OwnedBy: "codex"},
		{ID: "subscription/codex/gpt-5.6-terra", OwnedBy: "codex"},
		{ID: "gpt-5.6-terra", OwnedBy: "antigravity"},
	})
	standard, _ := modelvariants.CodexStandardAlias("gpt-5.6-terra")
	fast, _ := modelvariants.CodexFastAlias("gpt-5.6-terra")
	aliases := map[string]bool{}
	for channel, entries := range generated.Aliases {
		for _, entry := range entries {
			aliases[channel+"\x00"+entry.Alias] = true
			if strings.Contains(entry.Alias, "fast-fast") {
				t.Fatalf("recursive alias generated: %+v", entry)
			}
		}
	}
	for _, key := range []string{"codex\x00" + standard, "codex\x00" + fast, "codex\x00subscription/codex/custom-fast", "antigravity\x00subscription/gemini/gpt-5.6-terra"} {
		if !aliases[key] {
			t.Fatalf("missing generated alias %q: %+v", key, generated.Aliases)
		}
	}
	if len(generated.FastAliases) != 1 || generated.FastAliases[0] != fast {
		t.Fatalf("Fast aliases=%v", generated.FastAliases)
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

func assertManagedConfigSemantics(t *testing.T, raw []byte, desired managedConfig) {
	t.Helper()
	if err := verifyManagedConfig(raw, desired); err != nil {
		t.Fatal(err)
	}
}
