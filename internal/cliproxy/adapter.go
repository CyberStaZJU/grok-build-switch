package cliproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"grok_switch/internal/server"
)

const managementBaseURL = "http://127.0.0.1:8317/v0/management"

type Manager struct {
	Paths         Paths
	Runtime       Runtime
	Store         KeyStore
	BuiltinBinary string
	BuiltinHash   string
	HTTPClient    *http.Client
}

func NewManager(dataDir, home, builtin string, store KeyStore) *Manager {
	p := NewPaths(dataDir)
	return &Manager{Paths: p, Runtime: Runtime{Paths: p, Home: home}, Store: store, BuiltinBinary: builtin, BuiltinHash: BinarySHA256, HTTPClient: &http.Client{Timeout: 12 * time.Second}}
}

// ResolveBuiltinBinary uses the stable app-bundle location Contents/Resources/cliproxy/CLIProxyAPI.
func ResolveBuiltinBinary(executable string) string {
	macOSDir := filepath.Dir(executable)
	return filepath.Clean(filepath.Join(macOSDir, "..", "Resources", "cliproxy", "CLIProxyAPI"))
}

func (m *Manager) Status(ctx context.Context) (server.SubscriptionProxyStatus, error) {
	_, err := os.Stat(m.Paths.Binary)
	if errors.Is(err, os.ErrNotExist) {
		return server.SubscriptionProxyStatus{Installed: false, Running: false, Healthy: false, State: "stopped"}, nil
	}
	if err != nil {
		return server.SubscriptionProxyStatus{}, sanitize(err)
	}
	st, err := m.Runtime.Status(ctx)
	if err != nil {
		return server.SubscriptionProxyStatus{Installed: true, State: "error", LastError: err.Error()}, nil
	}
	state := "stopped"
	if st.Running && st.Healthy {
		state = "running"
	} else if st.Running {
		state = "running"
	}
	key, keyErr := m.keys()
	masked := ""
	if keyErr == nil && key.Inference != "" {
		masked = maskKey(key.Inference)
	}
	return server.SubscriptionProxyStatus{
		Installed:    true,
		Running:      st.Running,
		Healthy:      st.Healthy,
		Version:      Version,
		State:        state,
		PID:          st.PID,
		ConfigPath:   m.Paths.Config,
		LastError:    lastErrorString(st, keyErr),
		BaseURL:      managementBaseURL,
		APIKeyMasked: masked,
	}, nil
}

// lastErrorString surfaces the most relevant error from status checks.
func lastErrorString(st Status, keyErr error) string {
	if !st.Running {
		return ""
	}
	if !st.Healthy {
		return "服务进程存活但健康检查失败"
	}
	if keyErr != nil {
		return "密钥读取失败"
	}
	return ""
}

// maskKey returns a masked version of an API key, showing only first 4 and last 4 chars.
func maskKey(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

func (m *Manager) ServiceAction(ctx context.Context, action string) error {
	switch action {
	case "start":
		if err := m.prepare(); err != nil {
			return err
		}
		return sanitize(m.Runtime.Start(ctx))
	case "stop":
		return sanitize(m.Runtime.Stop(ctx))
	case "restart":
		// Stop before replacing the installed executable or LaunchAgent plist.
		// Updating a running binary first is unnecessary and can leave launchd
		// supervising stale state when the bundle was upgraded.
		if err := m.Runtime.Stop(ctx); err != nil {
			return sanitize(err)
		}
		if err := m.prepare(); err != nil {
			return err
		}
		return sanitize(m.Runtime.Start(ctx))
	default:
		return fmt.Errorf("无效服务操作")
	}
}

func (m *Manager) prepare() error {
	if m.Store == nil {
		return fmt.Errorf("系统钥匙串不可用")
	}
	if _, err := os.Stat(m.BuiltinBinary); err != nil {
		return fmt.Errorf("内置 CLIProxyAPI 不可用")
	}
	if err := InstallBuiltin(m.BuiltinBinary, m.Paths, m.BuiltinHash); err != nil {
		return sanitize(err)
	}
	keys, err := EnsureKeys(m.Store)
	if err != nil {
		return err
	}
	if err = WriteConfig(m.Paths, keys); err != nil {
		return sanitize(err)
	}
	return sanitize(m.Runtime.InstallAgent())
}

func (m *Manager) keys() (Keys, error) {
	if m.Store == nil {
		return Keys{}, fmt.Errorf("系统钥匙串不可用")
	}
	keys, err := EnsureKeys(m.Store)
	if err != nil {
		return Keys{}, err
	}
	return keys, nil
}

func (m *Manager) request(ctx context.Context, management bool, method, endpoint string, body any, out any) error {
	keys, err := m.keys()
	if err != nil {
		return err
	}
	var payload io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return fmt.Errorf("请求编码失败")
		}
		payload = bytes.NewReader(raw)
	}
	base := managementBaseURL
	if !management {
		base = "http://127.0.0.1:8317"
	}
	req, err := http.NewRequestWithContext(ctx, method, base+endpoint, payload)
	if err != nil {
		return fmt.Errorf("创建请求失败")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if management {
		req.Header.Set("Authorization", "Bearer "+keys.Management)
	} else {
		req.Header.Set("Authorization", "Bearer "+keys.Inference)
	}
	client := m.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("CLIProxyAPI 请求失败")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("读取 CLIProxyAPI 响应失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("CLIProxyAPI 返回 %s", resp.Status)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, out) != nil {
		return fmt.Errorf("CLIProxyAPI 响应格式无效")
	}
	return nil
}

var oauthEndpoints = map[string]string{"codex": "/codex-auth-url", "antigravity": "/antigravity-auth-url", "xai": "/xai-auth-url"}

func (m *Manager) StartLogin(ctx context.Context, provider string) (server.SubscriptionProxyLogin, error) {
	ep, ok := oauthEndpoints[provider]
	if !ok {
		return server.SubscriptionProxyLogin{}, fmt.Errorf("无效登录供应商")
	}
	// Ensure browser OAuth redirects to localhost:1455 can complete while
	// CLIProxyAPI runs headless under launchd.
	if err := ensureOAuthCallbackBridge(); err != nil {
		return server.SubscriptionProxyLogin{}, err
	}
	var raw map[string]any
	if err := m.request(ctx, true, http.MethodGet, ep, nil, &raw); err != nil {
		return server.SubscriptionProxyLogin{}, err
	}
	id := stringField(raw, "state", "id", "session_id")
	url := stringField(raw, "url", "auth_url", "verification_url")
	status := normalizeLoginStatus(stringField(raw, "status"), url != "", false)
	return server.SubscriptionProxyLogin{
		ID:              id,
		Provider:        provider,
		Status:          status,
		VerificationURL: url,
		UserCode:        stringField(raw, "user_code"),
		StatusMessage:   "请在浏览器中完成登录；可用第二个 ChatGPT 账号授权",
	}, nil
}

func (m *Manager) Login(ctx context.Context, id string) (server.SubscriptionProxyLogin, error) {
	var raw map[string]any
	if err := m.request(ctx, true, http.MethodGet, "/get-auth-status?state="+url.QueryEscape(id), nil, &raw); err != nil {
		return server.SubscriptionProxyLogin{}, err
	}
	status := normalizeLoginStatus(stringField(raw, "status"), false, true)
	msg := stringField(raw, "message", "error", "status_message")
	// Surface common network failures in plain Chinese.
	if status == "failed" {
		low := strings.ToLower(msg)
		switch {
		case strings.Contains(low, "deadline exceeded") || strings.Contains(low, "timeout") || strings.Contains(low, "i/o timeout"):
			msg = "换取登录令牌超时：订阅代理访问 OpenAI 失败，请确认本机代理（7890）可用后重试"
		case strings.Contains(low, "code_exchange_failed") || strings.Contains(low, "exchange"):
			msg = "换取登录令牌失败：请重新添加账号并在浏览器授权后尽快完成"
		case strings.Contains(low, "timed out") || strings.Contains(low, "authentication timed out"):
			msg = "登录超时：请重新点击添加账号，并在 3 分钟内完成浏览器授权"
		}
	}
	return server.SubscriptionProxyLogin{
		ID:            id,
		Provider:      stringField(raw, "provider"),
		Status:        status,
		StatusMessage: msg,
	}, nil
}

// normalizeLoginStatus maps CLIProxyAPI auth statuses into a stable UI vocabulary:
// pending | completed | failed | cancelled
//
// CLIProxyAPI returns:
//   - "ok" from codex-auth-url when the browser URL is ready (still pending login)
//   - "wait" from get-auth-status while the user is authorizing
//   - "ok" from get-auth-status after the account is saved
//   - "error" / timeout messages for failures
func normalizeLoginStatus(raw string, hasURL bool, fromPoll bool) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "wait", "waiting", "pending", "opening", "in_progress", "in-progress":
		return "pending"
	case "completed", "success", "done", "authenticated", "authorized":
		return "completed"
	case "error", "failed", "timeout", "timed_out", "expired":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "ok":
		if fromPoll {
			// Poll result "ok" means login finished.
			return "completed"
		}
		// Start result "ok" only means the authorize URL was issued.
		if hasURL {
			return "pending"
		}
		return "completed"
	case "":
		if hasURL {
			return "pending"
		}
		if fromPoll {
			return "pending"
		}
		return "pending"
	default:
		if hasURL && !fromPoll {
			return "pending"
		}
		return s
	}
}
func (m *Manager) CancelLogin(ctx context.Context, id string) error {
	return m.request(ctx, true, http.MethodPost, "/cancel-auth", map[string]string{"state": id}, nil)
}

func (m *Manager) Accounts(ctx context.Context) ([]server.SubscriptionProxyAccount, error) {
	var raw struct {
		Files []map[string]any `json:"files"`
	}
	if err := m.request(ctx, true, http.MethodGet, "/auth-files", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]server.SubscriptionProxyAccount, 0, len(raw.Files))
	for _, f := range raw.Files {
		id := stringField(f, "name", "id", "file")
		provider := canonical(stringField(f, "provider", "type"), id)
		out = append(out, server.SubscriptionProxyAccount{ID: id, Name: id, Provider: provider, Label: stringField(f, "label"), Email: stringField(f, "email"), Status: valueOr(stringField(f, "status"), "ready"), StatusMessage: stringField(f, "message"), Disabled: boolField(f, "disabled"), Unavailable: boolField(f, "unavailable")})
	}
	return out, nil
}
func (m *Manager) UpdateAccount(ctx context.Context, id, label string, disabled bool) (server.SubscriptionProxyAccount, error) {
	// CLIProxyAPI 的管理 API 不支持 PATCH，直接修改 auth 文件中的 disabled 字段。
	accounts, err := m.Accounts(ctx)
	if err != nil {
		return server.SubscriptionProxyAccount{}, err
	}
	var targetPath string
	var targetAccount *server.SubscriptionProxyAccount
	for i := range accounts {
		if accounts[i].ID == id {
			// 从 management API 返回的数据中没有文件路径，需要重新读取
			targetPath = filepath.Join(m.Paths.AuthDir, id)
			acc := accounts[i]
			targetAccount = &acc
			break
		}
	}
	if targetAccount == nil {
		return server.SubscriptionProxyAccount{}, os.ErrNotExist
	}

	if err := updateAuthFileDisabled(targetPath, disabled); err != nil {
		return server.SubscriptionProxyAccount{}, err
	}

	// 更新 label（如果提供）
	if label != "" {
		if err := updateAuthFileLabel(targetPath, label); err != nil {
			return server.SubscriptionProxyAccount{}, err
		}
		targetAccount.Label = label
	}

	targetAccount.Disabled = disabled
	if disabled {
		targetAccount.Status = "disabled"
	} else {
		targetAccount.Status = "active"
	}
	return *targetAccount, nil
}

// updateAuthFileDisabled 修改 auth JSON 文件中的 disabled 字段。
func updateAuthFileDisabled(path string, disabled bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 auth 文件失败: %w", err)
	}
	var auth map[string]any
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("解析 auth 文件失败: %w", err)
	}
	auth["disabled"] = disabled
	updated, err := json.MarshalIndent(auth, "", "    ")
	if err != nil {
		return fmt.Errorf("序列化 auth 文件失败: %w", err)
	}
	if err := atomicWrite(path, updated, 0o600); err != nil {
		return fmt.Errorf("写入 auth 文件失败: %w", err)
	}
	return nil
}

// updateAuthFileLabel 修改 auth JSON 文件中的 label 字段。
func updateAuthFileLabel(path, label string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取 auth 文件失败: %w", err)
	}
	var auth map[string]any
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("解析 auth 文件失败: %w", err)
	}
	auth["label"] = label
	updated, err := json.MarshalIndent(auth, "", "    ")
	if err != nil {
		return fmt.Errorf("序列化 auth 文件失败: %w", err)
	}
	if err := atomicWrite(path, updated, 0o600); err != nil {
		return fmt.Errorf("写入 auth 文件失败: %w", err)
	}
	return nil
}
func (m *Manager) DeleteAccount(ctx context.Context, id string) error {
	// 直接删除 auth 文件，避免 CLIProxyAPI 对查询参数中 + 号等字符的处理不一致
	path := filepath.Join(m.Paths.AuthDir, id)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("删除 auth 文件失败: %w", err)
	}
	return nil
}

type upstreamModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
}

type oauthModelAlias struct {
	Name         string `json:"name"`
	Alias        string `json:"alias"`
	Fork         bool   `json:"fork"`
	DisplayName  string `json:"display-name,omitempty"`
	ForceMapping bool   `json:"force-mapping,omitempty"`
}

type oauthModelAliases map[string][]oauthModelAlias

type oauthModelAliasesResponse struct {
	Aliases oauthModelAliases `json:"oauth-model-alias"`
}

var aliasChannels = map[string]string{
	"codex":  "codex",
	"gemini": "antigravity",
	"grok":   "xai",
}

func (m *Manager) getModels(ctx context.Context) ([]upstreamModel, error) {
	var raw struct {
		Data []upstreamModel `json:"data"`
	}
	if err := m.request(ctx, false, http.MethodGet, "/v1/models", nil, &raw); err != nil {
		return nil, err
	}
	return raw.Data, nil
}

// syncModelAliases merges the aliases owned by this application into
// CLIProxyAPI without disturbing aliases managed by users or other clients.
func (m *Manager) syncModelAliases(ctx context.Context, models []upstreamModel) (oauthModelAliases, error) {
	generated := generatedAliases(models)
	var response oauthModelAliasesResponse
	if err := m.request(ctx, true, http.MethodGet, "/oauth-model-alias", nil, &response); err != nil {
		return nil, err
	}
	current := response.Aliases
	if current == nil {
		current = oauthModelAliases{}
	}
	for _, channel := range aliasChannels {
		kept := make([]oauthModelAlias, 0, len(current[channel])+len(generated[channel]))
		for _, alias := range current[channel] {
			if !strings.HasPrefix(alias.Alias, "subscription/") {
				kept = append(kept, alias)
			}
		}
		kept = append(kept, generated[channel]...)
		current[channel] = kept
	}
	if err := m.request(ctx, true, http.MethodPut, "/oauth-model-alias", current, nil); err != nil {
		return nil, err
	}
	if err := saveModelAliases(m.Paths, current); err != nil {
		return nil, sanitize(err)
	}
	return generated, nil
}

func generatedAliases(models []upstreamModel) oauthModelAliases {
	out := oauthModelAliases{}
	seen := map[string]bool{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" || strings.HasPrefix(id, "subscription/") {
			continue
		}
		provider := canonical(model.OwnedBy, id)
		channel, ok := aliasChannels[provider]
		if !ok || seen[provider+"\x00"+id] {
			continue
		}
		seen[provider+"\x00"+id] = true
		alias := "subscription/" + provider + "/" + id
		out[channel] = append(out[channel], oauthModelAlias{Name: id, Alias: alias, Fork: true, DisplayName: id})
	}
	for channel := range out {
		sort.Slice(out[channel], func(i, j int) bool { return out[channel][i].Alias < out[channel][j].Alias })
	}
	return out
}

func subscriptionModels(models []upstreamModel) []server.SubscriptionProxyModel {
	seen := map[string]bool{}
	out := []server.SubscriptionProxyModel{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		provider := canonical(model.OwnedBy, id)
		if strings.HasPrefix(id, "subscription/") {
			parts := strings.SplitN(id, "/", 3)
			if len(parts) == 3 {
				provider, id = parts[1], parts[2]
			}
		}
		if id == "" {
			continue
		}
		if _, ok := aliasChannels[provider]; !ok || seen[provider+"\x00"+id] {
			continue
		}
		seen[provider+"\x00"+id] = true
		alias := "subscription/" + provider + "/" + id
		out = append(out, server.SubscriptionProxyModel{ID: alias, Provider: provider, Label: id})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Manager) Models(ctx context.Context) ([]server.SubscriptionProxyModel, error) {
	initial, err := m.getModels(ctx)
	if err != nil {
		return nil, err
	}
	if len(generatedAliases(initial)) == 0 {
		return subscriptionModels(initial), nil
	}
	if _, err = m.syncModelAliases(ctx, initial); err != nil {
		return nil, err
	}
	refreshed, err := m.getModels(ctx)
	if err != nil {
		return subscriptionModels(initial), nil
	}
	return subscriptionModels(refreshed), nil
}
func (m *Manager) InferenceKey(context.Context) (string, error) {
	keys, err := m.keys()
	if err != nil {
		return "", err
	}
	return keys.Inference, nil
}
func (m *Manager) Diagnostics(ctx context.Context) ([]server.SubscriptionProxyCheck, error) {
	st, err := m.Status(ctx)
	if err != nil {
		return nil, err
	}
	bundled := false
	if info, e := os.Stat(m.BuiltinBinary); e == nil && !info.IsDir() {
		bundled = true
	}
	checks := []server.SubscriptionProxyCheck{{Name: "bundled", Status: passFail(bundled), Message: map[bool]string{true: "内置资源可用", false: "内置资源不可用"}[bundled]}, {Name: "installed", Status: passFail(st.Installed)}, {Name: "running", Status: passFail(st.Running)}, {Name: "healthy", Status: passFail(st.Healthy)}}
	return checks, nil
}

func (m *Manager) SelectedModels() ([]server.SubscriptionProxyModel, error) {
	var v []server.SubscriptionProxyModel
	raw, err := os.ReadFile(filepath.Join(m.Paths.Root, "selected-models.json"))
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return nil, sanitize(err)
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil, fmt.Errorf("模型选择文件格式无效")
	}
	return v, nil
}
func (m *Manager) SetSelectedModels(v []server.SubscriptionProxyModel) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("保存模型选择失败")
	}
	return sanitize(atomicWrite(filepath.Join(m.Paths.Root, "selected-models.json"), raw, 0o600))
}

func sanitize(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("CLIProxyAPI 操作失败")
}
func stringField(m map[string]any, ks ...string) string {
	for _, k := range ks {
		if v, ok := m[k].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func boolField(m map[string]any, k string) bool { v, _ := m[k].(bool); return v }
func valueOr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
func passFail(v bool) string {
	if v {
		return "pass"
	}
	return "fail"
}
func canonical(provider, hint string) string {
	s := strings.ToLower(provider + " " + hint)
	switch {
	case strings.Contains(s, "codex") || strings.Contains(s, "openai"):
		return "codex"
	case strings.Contains(s, "antigravity") || strings.Contains(s, "gemini") || strings.Contains(s, "google"):
		return "gemini"
	case strings.Contains(s, "xai") || strings.Contains(s, "grok"):
		return "grok"
	}
	return provider
}
