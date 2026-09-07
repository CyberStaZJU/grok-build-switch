package cliproxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"grok_switch/internal/server"
)

const googleCreditsURL = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
const quotaReadLimit = 1 << 20

type quotaAuthFileResponse struct {
	Files []quotaAuthFile `json:"files"`
}

type quotaAuthFile struct {
	ID             string                      `json:"id"`
	Name           string                      `json:"name"`
	Provider       string                      `json:"provider"`
	Label          string                      `json:"label"`
	Email          string                      `json:"email"`
	Status         string                      `json:"status"`
	StatusMessage  string                      `json:"status_message"`
	Disabled       bool                        `json:"disabled"`
	Unavailable    bool                        `json:"unavailable"`
	NextRetryAfter string                      `json:"next_retry_after"`
	Quota          quotaObservation            `json:"quota"`
	ModelQuotas    map[string]quotaObservation `json:"model_quotas"`
	IDToken        map[string]any              `json:"id_token"`
}

type quotaObservation struct {
	ObservedAt string            `json:"observed_at"`
	Signals    map[string]string `json:"signals"`
}

type googleAuthFile struct {
	AccessToken string `json:"access_token"`
}

type googleCreditsResponse struct {
	PaidTier *struct {
		ID               string `json:"id"`
		AvailableCredits []struct {
			CreditType                  string `json:"creditType"`
			CreditAmount                string `json:"creditAmount"`
			MinimumCreditAmountForUsage string `json:"minimumCreditAmountForUsage"`
		} `json:"availableCredits"`
	} `json:"paidTier"`
}

func (m *Manager) Quotas(ctx context.Context) ([]server.SubscriptionQuotaAccount, error) {
	var raw quotaAuthFileResponse
	if err := m.request(ctx, true, http.MethodGet, "/auth-files", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]server.SubscriptionQuotaAccount, 0, len(raw.Files))
	for _, auth := range raw.Files {
		provider, trusted := trustedCatalogProvider(auth.Provider)
		if !trusted || (provider != "codex" && provider != "gemini") {
			continue
		}
		account := server.SubscriptionQuotaAccount{
			ID: auth.ID, Provider: provider, Label: auth.Label, Email: auth.Email,
			Status: auth.Status, Disabled: auth.Disabled, Unavailable: auth.Unavailable,
			NextRetryAt: normalizeResetAt(auth.NextRetryAfter),
		}
		if provider == "codex" {
			account.Source = "codex_upstream_observation"
			account.Plan = valueOr(signalValue(auth.Quota.Signals, "x-codex-plan-type"), mapString(auth.IDToken, "plan_type"))
			account.ObservedAt = auth.Quota.ObservedAt
			account.Windows = codexQuotaWindows(auth.Quota.Signals)
			account.Credits = codexCredits(auth.Quota.Signals)
			models := make([]string, 0, len(auth.ModelQuotas))
			for model := range auth.ModelQuotas {
				models = append(models, model)
			}
			sort.Strings(models)
			for _, model := range models {
				observation := auth.ModelQuotas[model]
				for _, window := range codexQuotaWindows(observation.Signals) {
					window.Name = fmt.Sprintf("model %q / %s (observed: %s)", model, window.Name, valueOr(normalizeResetAt(observation.ObservedAt), "unknown"))
					account.Windows = append(account.Windows, window)
				}
			}
			if len(account.Windows) == 0 && account.Credits == nil {
				account.Message = valueOr(account.Message, "尚未从上游响应中观测到额度水位")
			}
		} else {
			account.Source = "google_load_code_assist"
			account.Message = "Google 额度未知（unknown）：账号未处于可查询的 active 状态"
			if !account.Disabled && !account.Unavailable && strings.EqualFold(strings.TrimSpace(account.Status), "active") {
				credits, observedAt, err := m.googleCredits(ctx, auth.ID)
				if err == nil {
					account.Credits = credits
					account.ObservedAt = observedAt
					account.Message = ""
					if credits == nil {
						account.Message = "Google 额度未知（unknown）：响应未提供 Google One AI credits"
					}
				} else {
					account.Message = "Google 额度未知（unknown）：未提供可读取的订阅额度"
				}
			}
		}
		out = append(out, account)
	}
	return out, nil
}

func mapString(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func signalValue(signals map[string]string, name string) string {
	for key, value := range signals {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func codexCredits(signals map[string]string) *server.SubscriptionQuotaCredits {
	balance := signalValue(signals, "x-codex-credits-balance")
	if balance == "" {
		return nil
	}
	remaining, valid := quotaNumber(balance)
	if !valid {
		return nil
	}
	return &server.SubscriptionQuotaCredits{Type: "CODEX_CREDITS", Remaining: remaining}
}

func codexQuotaWindows(signals map[string]string) []server.SubscriptionQuotaWindow {
	type windowFields struct {
		used, minutes, resetAt, resetAfter, reached string
	}
	windows := map[string]*windowFields{}
	for rawName, value := range signals {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if !strings.HasPrefix(name, "x-codex-") {
			continue
		}
		base, field := codexSignalParts(strings.TrimPrefix(name, "x-codex-"))
		if base == "" || field == "" {
			continue
		}
		entry := windows[base]
		if entry == nil {
			entry = &windowFields{}
			windows[base] = entry
		}
		switch field {
		case "used-percent":
			entry.used = value
		case "window-minutes":
			entry.minutes = value
		case "reset-at":
			entry.resetAt = value
		case "reset-after-seconds":
			entry.resetAfter = value
		case "limit-reached":
			entry.reached = value
		}
	}
	keys := make([]string, 0, len(windows))
	for key := range windows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]server.SubscriptionQuotaWindow, 0, len(keys))
	for _, key := range keys {
		fields := windows[key]
		window := server.SubscriptionQuotaWindow{Name: strings.ReplaceAll(key, "-", " ")}
		if value, valid := quotaNumber(fields.used); valid {
			window.UsedPercent = &value
		}
		if value, err := strconv.ParseInt(strings.TrimSpace(fields.minutes), 10, 64); err == nil && value >= 0 {
			window.WindowMinutes = &value
		}
		if value, err := strconv.ParseInt(strings.TrimSpace(fields.resetAfter), 10, 64); err == nil && value >= 0 {
			window.ResetAfter = &value
		}
		if value, err := strconv.ParseBool(strings.TrimSpace(fields.reached)); err == nil {
			window.LimitReached = &value
		}
		window.ResetAt = normalizeResetAt(fields.resetAt)
		if window.UsedPercent != nil || window.WindowMinutes != nil || window.ResetAt != "" || window.ResetAfter != nil || window.LimitReached != nil {
			out = append(out, window)
		}
	}
	return out
}

func codexSignalParts(value string) (string, string) {
	for _, field := range []string{"reset-after-seconds", "window-minutes", "limit-reached", "used-percent", "reset-at"} {
		if strings.HasSuffix(value, "-"+field) {
			return strings.TrimSuffix(value, "-"+field), field
		}
	}
	return "", ""
}

func normalizeResetAt(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		if seconds <= 253402300799 {
			return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
		}
		return ""
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.Format(time.RFC3339)
	}
	return ""
}

func (m *Manager) googleCredits(ctx context.Context, id string) (*server.SubscriptionQuotaCredits, string, error) {
	path, err := accountFilePath(m.Paths.AuthDir, id)
	if err != nil {
		return nil, "", err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", fmt.Errorf("Google 凭据不可用")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > quotaReadLimit {
		return nil, "", fmt.Errorf("Google 凭据不可用")
	}
	raw, err := readQuotaBytes(file)
	if err != nil {
		return nil, "", err
	}
	var auth googleAuthFile
	if json.Unmarshal(raw, &auth) != nil || strings.TrimSpace(auth.AccessToken) == "" {
		return nil, "", fmt.Errorf("Google 凭据不可用")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, googleCreditsURL, bytes.NewBufferString(`{"metadata":{"ideType":"ANTIGRAVITY"}}`))
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(auth.AccessToken))
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "antigravity")
	client := m.googleQuotaHTTPClient()
	if m.QuotaHTTPClient == nil {
		defer client.CloseIdleConnections()
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	body, err := readQuotaBytes(response.Body)
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("Google 额度查询失败")
	}
	var payload googleCreditsResponse
	if json.Unmarshal(body, &payload) != nil || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return nil, "", fmt.Errorf("Google 额度响应无效")
	}
	observedAt := time.Now().UTC().Format(time.RFC3339)
	if payload.PaidTier == nil {
		return nil, observedAt, nil
	}
	for _, item := range payload.PaidTier.AvailableCredits {
		if !strings.EqualFold(strings.TrimSpace(item.CreditType), "GOOGLE_ONE_AI") {
			continue
		}
		remaining, validRemaining := quotaNumber(item.CreditAmount)
		minimum, validMinimum := quotaNumber(item.MinimumCreditAmountForUsage)
		if !validRemaining || !validMinimum {
			return nil, "", fmt.Errorf("Google 额度数值无效")
		}
		return &server.SubscriptionQuotaCredits{Type: "GOOGLE_ONE_AI", Remaining: remaining, MinimumPerUse: minimum, Tier: strings.TrimSpace(payload.PaidTier.ID)}, observedAt, nil
	}
	return nil, observedAt, nil
}

func (m *Manager) googleQuotaHTTPClient() *http.Client {
	if m.QuotaHTTPClient != nil {
		client := *m.QuotaHTTPClient
		client.CheckRedirect = rejectQuotaRedirect
		client.Jar = nil
		if client.Timeout <= 0 || client.Timeout > 12*time.Second {
			client.Timeout = 12 * time.Second
		}
		return &client
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if raw := strings.TrimSpace(localProxyURL()); raw != "" {
		if parsed, err := url.Parse(raw); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	return &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: rejectQuotaRedirect}
}

func rejectQuotaRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func readQuotaBytes(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, quotaReadLimit+1))
	if err != nil || len(body) > quotaReadLimit {
		return nil, fmt.Errorf("额度数据读取失败或超出大小限制")
	}
	return body, nil
}

func quotaNumber(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
