package cliproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"sync"
	"time"

	"grok_switch/internal/modelvariants"
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
	opMu          sync.Mutex
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
	m.opMu.Lock()
	defer m.opMu.Unlock()
	unlock, err := acquireConfigOperationLock(m.Paths)
	if err != nil {
		return sanitize(err)
	}
	defer unlock()

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
	var rawBody []byte
	if body != nil {
		var err error
		rawBody, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("请求编码失败")
		}
	}
	raw, err := m.requestRaw(ctx, management, method, endpoint, "application/json", rawBody, 1<<20)
	if err != nil {
		return err
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, out) != nil {
		return fmt.Errorf("CLIProxyAPI 响应格式无效")
	}
	return nil
}

func (m *Manager) requestRaw(ctx context.Context, management bool, method, endpoint, contentType string, body []byte, responseLimit int64) ([]byte, error) {
	keys, err := m.keys()
	if err != nil {
		return nil, err
	}
	base := managementBaseURL
	if !management {
		base = "http://127.0.0.1:8317"
	}
	var payload io.Reader
	if body != nil {
		payload = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+endpoint, payload)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败")
	}
	if body != nil && contentType != "" {
		req.Header.Set("Content-Type", contentType)
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
		return nil, fmt.Errorf("CLIProxyAPI 请求失败")
	}
	defer resp.Body.Close()
	if responseLimit <= 0 {
		responseLimit = 1 << 20
	}
	limited := io.LimitReader(resp.Body, responseLimit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("读取 CLIProxyAPI 响应失败")
	}
	if int64(len(raw)) > responseLimit {
		return nil, fmt.Errorf("CLIProxyAPI 响应过大")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("CLIProxyAPI 返回 %s", resp.Status)
	}
	return raw, nil
}

func (m *Manager) getFullConfigYAML(ctx context.Context) ([]byte, error) {
	return m.requestRaw(ctx, true, http.MethodGet, "/config.yaml", "", nil, 16<<20)
}

func (m *Manager) putFullConfigYAML(ctx context.Context, raw []byte) error {
	_, err := m.requestRaw(ctx, true, http.MethodPut, "/config.yaml", "application/yaml", raw, 1<<20)
	return err
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
		if _, err := accountFilePath(m.Paths.AuthDir, id); err != nil {
			return nil, fmt.Errorf("CLIProxyAPI 返回无效账号标识")
		}
		provider, _ := trustedCatalogProvider(stringField(f, "provider", "type"))
		out = append(out, server.SubscriptionProxyAccount{ID: id, Name: id, Provider: provider, Label: stringField(f, "label"), Email: stringField(f, "email"), Status: valueOr(stringField(f, "status"), "ready"), StatusMessage: stringField(f, "message"), Disabled: boolField(f, "disabled"), Unavailable: boolField(f, "unavailable")})
	}
	return out, nil
}
func (m *Manager) UpdateAccount(ctx context.Context, id, label string, disabled bool) (server.SubscriptionProxyAccount, error) {
	// CLIProxyAPI 的管理 API 不支持 PATCH，直接修改 auth 文件中的 disabled 字段。
	targetPath, err := accountFilePath(m.Paths.AuthDir, id)
	if err != nil {
		return server.SubscriptionProxyAccount{}, err
	}
	accounts, err := m.Accounts(ctx)
	if err != nil {
		return server.SubscriptionProxyAccount{}, err
	}
	var targetAccount *server.SubscriptionProxyAccount
	for i := range accounts {
		if accounts[i].ID == id {
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
	// 直接删除 auth 文件，避免 CLIProxyAPI 对查询参数中 + 号等字符的处理不一致。
	// 删除前必须由当前可信账号目录确认该 ID，不能把调用者输入直接解释为路径。
	path, err := accountFilePath(m.Paths.AuthDir, id)
	if err != nil {
		return err
	}
	accounts, err := m.Accounts(ctx)
	if err != nil {
		return err
	}
	known := false
	for _, account := range accounts {
		if account.ID == id {
			known = true
			break
		}
	}
	if !known {
		return os.ErrNotExist
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("删除 auth 文件失败: %w", err)
	}
	return nil
}

func accountFilePath(authDir, id string) (string, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsRune(id, '\x00') || strings.ContainsAny(id, `/\\`) || filepath.Base(id) != id || filepath.Clean(id) != id || filepath.IsAbs(id) || filepath.VolumeName(id) != "" {
		return "", fmt.Errorf("无效账号标识")
	}
	root, err := filepath.Abs(authDir)
	if err != nil {
		return "", fmt.Errorf("解析账号目录失败: %w", err)
	}
	path := filepath.Join(root, id)
	if filepath.Dir(path) != root {
		return "", fmt.Errorf("账号路径超出受管目录")
	}
	info, err := os.Lstat(path)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", fmt.Errorf("账号文件不是普通文件")
	}
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("检查账号文件失败: %w", err)
	}
	return path, nil
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

// syncModelAliases performs one full-config YAML update so aliases and the Fast
// service-tier rule become visible together. The caller must hold opMu across
// catalog discovery and this commit so an older snapshot cannot be serialized
// after a newer one. CLIProxyAPI 7.2.94 still has no ETag/CAS; the second GET
// narrows but cannot eliminate races with unrelated external writers.
func (m *Manager) syncModelAliases(ctx context.Context, models []upstreamModel) (oauthModelAliases, error) {
	previous, err := previousConfigOwnership(m.Paths)
	if err != nil {
		return nil, sanitize(err)
	}
	return m.syncModelAliasesFromOwnership(ctx, models, previous)
}

func (m *Manager) syncModelAliasesFromOwnership(ctx context.Context, models []upstreamModel, previous configOwnership) (oauthModelAliases, error) {
	desired := generatedManagedConfig(models)
	current, err := m.getFullConfigYAML(ctx)
	if err != nil {
		return nil, err
	}
	merged, nextOwnership, err := mergeManagedConfig(current, desired, previous, nil)
	if err != nil {
		return nil, sanitize(err)
	}

	// CLIProxyAPI has no revision/ETag. Re-read once immediately before PUT and
	// rebase if another editor changed the document during the first merge.
	latest, err := m.getFullConfigYAML(ctx)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(current, latest) {
		current = latest
		merged, nextOwnership, err = mergeManagedConfig(current, desired, previous, nil)
		if err != nil {
			return nil, sanitize(err)
		}
	}
	readConfig := func() ([]byte, bool, error) {
		raw, err := m.getFullConfigYAML(ctx)
		return raw, err == nil, err
	}
	writeConfig := func(raw []byte, exists bool) error {
		if !exists {
			return fmt.Errorf("CLIProxyAPI 远程配置不能删除")
		}
		return m.putFullConfigYAML(ctx, raw)
	}
	if err := commitConfigAndOwnership(m.Paths, current, true, merged, nextOwnership, readConfig, writeConfig); err != nil {
		return nil, sanitize(err)
	}
	verified, err := m.getFullConfigYAML(ctx)
	if err != nil {
		return nil, err
	}
	if err := verifyManagedConfig(verified, desired); err != nil {
		return nil, sanitize(err)
	}
	return desired.Aliases, nil
}

func generatedManagedConfig(models []upstreamModel) managedConfig {
	out := managedConfig{Aliases: oauthModelAliases{}}
	seen := map[string]bool{}
	fastSeen := map[string]bool{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" || strings.HasPrefix(id, "subscription/") {
			continue
		}
		provider, trusted := trustedCatalogProvider(model.OwnedBy)
		if !trusted {
			continue
		}
		if provider == "codex" {
			if _, generatedFastLeaf := modelvariants.TrustedCodexPhysicalFromFastLeaf(id); generatedFastLeaf || isRecursiveTrustedCodexFastLeaf(id) {
				continue
			}
		}
		channel, ok := aliasChannels[provider]
		if !ok || seen[provider+"\x00"+id] {
			continue
		}
		seen[provider+"\x00"+id] = true
		standard := "subscription/" + provider + "/" + id
		out.Aliases[channel] = append(out.Aliases[channel], oauthModelAlias{Name: id, Alias: standard, Fork: true, DisplayName: id})
		if provider == "codex" && modelvariants.IsTrustedCodexPhysicalModel(id) {
			fast, _ := modelvariants.CodexFastAlias(id)
			out.Aliases[channel] = append(out.Aliases[channel], oauthModelAlias{Name: id, Alias: fast, Fork: true, DisplayName: id})
			if !fastSeen[fast] {
				fastSeen[fast] = true
				out.FastAliases = append(out.FastAliases, fast)
			}
		}
	}
	for channel := range out.Aliases {
		sort.Slice(out.Aliases[channel], func(i, j int) bool { return out.Aliases[channel][i].Alias < out.Aliases[channel][j].Alias })
	}
	sort.Strings(out.FastAliases)
	return out
}

func isRecursiveTrustedCodexFastLeaf(id string) bool {
	const suffix = "-fast"
	if !strings.HasSuffix(id, suffix) {
		return false
	}
	_, ok := modelvariants.TrustedCodexPhysicalFromFastLeaf(strings.TrimSuffix(id, suffix))
	return ok
}

func subscriptionModels(models []upstreamModel) []server.SubscriptionProxyModel {
	seen := map[string]bool{}
	out := []server.SubscriptionProxyModel{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		provider, trusted := trustedCatalogProvider(model.OwnedBy)
		if !trusted {
			continue
		}
		if strings.HasPrefix(id, "subscription/") {
			parts := strings.SplitN(id, "/", 3)
			if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" || parts[1] != provider {
				continue
			}
			id = parts[2]
			if provider == "codex" {
				if _, generatedFast := modelvariants.TrustedCodexPhysicalFromFastAlias("subscription/codex/" + id); generatedFast || isRecursiveTrustedCodexFastLeaf(id) {
					continue
				}
			}
		} else {
			if !trusted {
				continue
			}
			if provider == "codex" {
				if _, generatedFastLeaf := modelvariants.TrustedCodexPhysicalFromFastLeaf(id); generatedFastLeaf || isRecursiveTrustedCodexFastLeaf(id) {
					continue
				}
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
	models, err := m.getModels(ctx)
	if err != nil {
		return nil, err
	}
	return subscriptionModels(models), nil
}

// ReconcileModels is the explicit mutating catalog operation. Status and other
// GET paths call Models, which is read-only. Catalog acquisition is covered by
// opMu so an older snapshot cannot be committed after a newer one.
func (m *Manager) ReconcileModels(ctx context.Context) ([]server.SubscriptionProxyModel, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	unlock, err := acquireConfigOperationLock(m.Paths)
	if err != nil {
		return nil, sanitize(err)
	}
	defer unlock()

	initial, err := m.getModels(ctx)
	if err != nil {
		return nil, err
	}
	previous, err := previousConfigOwnership(m.Paths)
	if err != nil {
		return nil, sanitize(err)
	}
	desired := generatedManagedConfig(initial)
	if len(desired.Aliases) == 0 && len(previous.Aliases) == 0 && previous.FastRuleFingerprint == "" {
		return subscriptionModels(initial), nil
	}
	if _, err = m.syncModelAliasesFromOwnership(ctx, initial, previous); err != nil {
		return nil, err
	}
	refreshed, err := m.waitForManagedModelConvergence(ctx, initial, previous)
	if err != nil {
		return nil, err
	}
	return subscriptionModels(refreshed), nil
}

func (m *Manager) waitForManagedModelConvergence(ctx context.Context, source []upstreamModel, previous configOwnership) ([]upstreamModel, error) {
	desired := generatedManagedConfig(source)
	if len(desired.Aliases) == 0 && len(previous.Aliases) == 0 {
		return source, nil
	}
	var previousFingerprint string
	stable := 0
	for attempt := 0; attempt < 20; attempt++ {
		models, err := m.getModels(ctx)
		if err == nil && rawModelCatalogMatchesOwnership(models, desired, previous) {
			fingerprint := rawModelCatalogFingerprint(models)
			if fingerprint == previousFingerprint {
				stable++
			} else {
				previousFingerprint = fingerprint
				stable = 1
			}
			if stable >= 2 {
				return models, nil
			}
		} else {
			previousFingerprint = ""
			stable = 0
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("CLIProxyAPI 模型目录收敛超时")
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("CLIProxyAPI 模型目录未收敛到受管别名")
}

func rawModelCatalogSatisfies(models []upstreamModel, desired managedConfig) bool {
	present := map[string]bool{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" || isRecursiveTrustedCodexFastLeaf(id) {
			continue
		}
		present[id] = true
	}
	for _, entries := range desired.Aliases {
		for _, alias := range entries {
			if !present[alias.Alias] {
				return false
			}
		}
	}
	return true
}

func rawModelCatalogMatchesOwnership(models []upstreamModel, desired managedConfig, previous configOwnership) bool {
	if !rawModelCatalogSatisfies(models, desired) {
		return false
	}
	present := map[string]bool{}
	for _, model := range models {
		if id := strings.TrimSpace(model.ID); id != "" {
			present[id] = true
		}
	}
	desiredAliases := map[string]bool{}
	for _, entries := range desired.Aliases {
		for _, alias := range entries {
			desiredAliases[alias.Alias] = true
		}
	}
	for _, identity := range previous.Aliases {
		if !desiredAliases[identity.Alias] && present[identity.Alias] {
			return false
		}
	}
	return true
}

func rawModelCatalogFingerprint(models []upstreamModel) string {
	values := make([]string, 0, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		owner := strings.ToLower(strings.TrimSpace(model.OwnedBy))
		if id != "" {
			values = append(values, owner+"\x00"+id)
		}
	}
	sort.Strings(values)
	hash := sha256.New()
	for _, value := range values {
		_, _ = io.WriteString(hash, value+"\n")
	}
	return hex.EncodeToString(hash.Sum(nil))
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
func trustedCatalogProvider(provider string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", "codex":
		return "codex", true
	case "antigravity":
		return "gemini", true
	case "xai":
		return "grok", true
	default:
		return "", false
	}
}
