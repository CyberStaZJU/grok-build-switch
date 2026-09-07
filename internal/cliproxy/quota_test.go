package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodexQuotaWindowsParsesIndependentPools(t *testing.T) {
	windows := codexQuotaWindows(map[string]string{
		"X-Codex-Primary-Used-Percent":                    "51",
		"X-Codex-Primary-Window-Minutes":                  "10080",
		"X-Codex-Primary-Reset-At":                        "1787588999",
		"X-Codex-Bengalfox-Secondary-Used-Percent":        "35",
		"X-Codex-Bengalfox-Secondary-Reset-After-Seconds": "600",
	})
	if len(windows) != 2 {
		t.Fatalf("windows=%#v", windows)
	}
	if windows[0].Name != "bengalfox secondary" || windows[0].UsedPercent == nil || *windows[0].UsedPercent != 35 || windows[0].ResetAfter == nil || *windows[0].ResetAfter != 600 {
		t.Fatalf("secondary=%#v", windows[0])
	}
	if windows[1].Name != "primary" || windows[1].UsedPercent == nil || *windows[1].UsedPercent != 51 || windows[1].WindowMinutes == nil || *windows[1].WindowMinutes != 10080 || windows[1].ResetAt == "" {
		t.Fatalf("primary=%#v", windows[1])
	}
}

func TestManagerQuotasReadsCodexSignalsAndGoogleCredits(t *testing.T) {
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer google-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]any{"paidTier": map[string]any{
			"id": "tier-1", "availableCredits": []map[string]string{{
				"creditType": "GOOGLE_ONE_AI", "creditAmount": "25000", "minimumCreditAmountForUsage": "50",
			}},
		}})
	}))
	defer google.Close()

	fixture := &configYAMLFixture{t: t, yaml: []byte("host: 127.0.0.1\n")}
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		if r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
			{"id": "codex.json", "provider": "codex", "email": "codex@example.com", "status": "active", "id_token": map[string]any{"plan_type": "pro"}, "quota": map[string]any{"observed_at": "2026-09-07T10:00:00Z", "signals": map[string]string{"X-Codex-Primary-Used-Percent": "42"}}},
			{"id": "google.json", "provider": "antigravity", "email": "google@example.com", "status": "active"},
		}})
	}))
	m.QuotaHTTPClient = google.Client()
	m.Paths.AuthDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(m.Paths.AuthDir, "google.json"), []byte(`{"access_token":"google-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Route the fixed production URL through the test server.
	m.QuotaHTTPClient.Transport = rewriteTransport{target: mustParseURL(t, google.URL), base: http.DefaultTransport}

	accounts, err := m.Quotas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].Plan != "pro" || len(accounts[0].Windows) != 1 {
		t.Fatalf("accounts=%#v", accounts)
	}
	if accounts[1].Credits == nil || accounts[1].Credits.Remaining != 25000 || accounts[1].Credits.MinimumPerUse != 50 {
		t.Fatalf("google credits=%#v", accounts[1])
	}
}

func TestQuotaRejectsMaliciousNumbers(t *testing.T) {
	for _, raw := range []string{"-1", "NaN", "Inf", "9223372036854775807"} {
		if normalizeResetAt(raw) != "" {
			t.Fatalf("invalid reset timestamp accepted: %s", raw)
		}
	}
	for _, raw := range []string{"NaN", "Inf", "+Inf", "-Inf", "-1", "1e999", "bad"} {
		t.Run(raw, func(t *testing.T) {
			if codexCredits(map[string]string{"x-codex-credits-balance": raw}) != nil {
				t.Fatal("invalid balance accepted")
			}
			if windows := codexQuotaWindows(map[string]string{"x-codex-primary-used-percent": raw, "x-codex-primary-window-minutes": raw, "x-codex-primary-reset-after-seconds": raw}); len(windows) != 0 {
				t.Fatalf("invalid windows: %#v", windows)
			}
			for _, field := range []string{"creditAmount", "minimumCreditAmountForUsage"} {
				item := map[string]string{"creditType": "GOOGLE_ONE_AI", "creditAmount": "1", "minimumCreditAmountForUsage": "1"}
				item[field] = raw
				body, _ := json.Marshal(map[string]any{"paidTier": map[string]any{"availableCredits": []any{item}}})
				m := quotaGoogleFixture(t, string(body), 200)
				if credits, _, err := m.googleCredits(context.Background(), "google.json"); err == nil || credits != nil {
					t.Fatalf("invalid Google %s accepted", field)
				}
			}
		})
	}
	if credits := codexCredits(map[string]string{"x-codex-credits-balance": "0"}); credits == nil || credits.Remaining != 0 {
		t.Fatal("zero is a valid observed balance")
	}
}

func quotaGoogleFixture(t *testing.T, body string, status int) *Manager {
	t.Helper()
	m := &Manager{}
	m.Paths.AuthDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(m.Paths.AuthDir, "google.json"), []byte(`{"access_token":"synthetic-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	m.QuotaHTTPClient = &http.Client{Transport: quotaRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != googleCreditsURL || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Errorf("unexpected quota request")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	return m
}

type quotaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f quotaRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGoogleQuotaUnknownAndBoundedReads(t *testing.T) {
	for _, body := range []string{`{}`, `{"paidTier":{}}`, `{"paidTier":{"availableCredits":[]}}`} {
		m := quotaGoogleFixture(t, body, 200)
		credits, observed, err := m.googleCredits(context.Background(), "google.json")
		if _, parseErr := time.Parse(time.RFC3339, observed); err != nil || credits != nil || parseErr != nil {
			t.Fatalf("unknown observation lost: %v %q %v", credits, observed, err)
		}
	}
	for _, body := range []string{"null", `{`, `{}` + strings.Repeat(" ", quotaReadLimit)} {
		m := quotaGoogleFixture(t, body, 200)
		if _, _, err := m.googleCredits(context.Background(), "google.json"); err == nil {
			t.Fatal("invalid or oversized response accepted")
		}
	}
	m := quotaGoogleFixture(t, `{}`, 200)
	if err := os.WriteFile(filepath.Join(m.Paths.AuthDir, "google.json"), []byte(`{"access_token":"synthetic-token"}`+strings.Repeat(" ", quotaReadLimit)), 0600); err != nil {
		t.Fatal(err)
	}
	m.QuotaHTTPClient.Transport = quotaRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("oversized credential was queried")
		return nil, nil
	})
	if _, _, err := m.googleCredits(context.Background(), "google.json"); err == nil {
		t.Fatal("oversized credentials accepted")
	}
}

type quotaTrackingBody struct {
	io.Reader
	closed bool
}

func (b *quotaTrackingBody) Close() error { b.closed = true; return nil }

type quotaTrackingTransport struct {
	body   *quotaTrackingBody
	closed bool
}

func (tr *quotaTrackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: tr.body, Header: make(http.Header), Request: r}, nil
}

func (tr *quotaTrackingTransport) CloseIdleConnections() { tr.closed = true }

func TestGoogleQuotaClosesBodyWithoutClosingBorrowedTransport(t *testing.T) {
	for _, body := range []string{`{}`, `invalid`, strings.Repeat("x", quotaReadLimit+1)} {
		m := quotaGoogleFixture(t, body, 200)
		transport := &quotaTrackingTransport{body: &quotaTrackingBody{Reader: strings.NewReader(body)}}
		m.QuotaHTTPClient.Transport = transport
		m.googleCredits(context.Background(), "google.json")
		if !transport.body.closed || transport.closed {
			t.Fatal("incorrect response/transport ownership")
		}
	}
}

func TestGoogleQuotaRejectsRedirects(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var leaked, requests atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Redirect(w, r, target.URL, code)
			}))
			defer origin.Close()
			m := quotaGoogleFixture(t, `{}`, 200)
			transport := http.DefaultTransport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			m.QuotaHTTPClient.Transport = rewriteTransport{target: mustParseURL(t, origin.URL), base: transport}
			if _, _, err := m.googleCredits(context.Background(), "google.json"); err == nil {
				t.Fatal("redirect accepted")
			}
			if leaked.Load() != 0 || requests.Load() != 1 {
				t.Fatal("redirect target contacted")
			}
			if m.QuotaHTTPClient.CheckRedirect != nil || m.QuotaHTTPClient.Timeout != 0 {
				t.Fatal("shared client mutated")
			}
		})
	}
}

func TestManagerQuotaTrustUnknownAndModelIsolation(t *testing.T) {
	files := []map[string]any{
		{"id": "google.json", "provider": "antigravity", "status": "active", "status_message": "secret-token"},
		{"id": "google.json", "provider": "antigravity", "status": "pending", "status_message": "secret-token"},
		{"id": "google.json", "provider": "antigravity", "status": "", "status_message": "secret-token"},
		{"id": "google.json", "provider": "antigravity", "status": "active", "disabled": true},
		{"id": "google.json", "provider": "antigravity", "status": "active", "unavailable": true},
		{"id": "google.json", "provider": "gemini", "status": "active"},
		{"id": "codex.json", "provider": "codex", "status": "active", "status_message": "secret-token", "model_quotas": map[string]any{
			"model-a": quotaObservation{ObservedAt: "2026-09-07T10:00:00Z", Signals: map[string]string{"x-codex-primary-used-percent": "20", "x-codex-credits-balance": "100"}},
			"model-b": quotaObservation{ObservedAt: "2026-09-07T11:00:00Z", Signals: map[string]string{"x-codex-primary-used-percent": "70"}},
		}},
	}
	fixture := &configYAMLFixture{t: t, yaml: []byte("host: 127.0.0.1\n")}
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"files": files})
	}))
	google := quotaGoogleFixture(t, `{}`, 200)
	m.Paths.AuthDir = google.Paths.AuthDir
	calls := 0
	base := google.QuotaHTTPClient.Transport
	m.QuotaHTTPClient = &http.Client{Transport: quotaRoundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return base.RoundTrip(r) })}
	accounts, err := m.Quotas(context.Background())
	if err != nil || len(accounts) != 6 || calls != 1 {
		t.Fatalf("accounts=%#v calls=%d err=%v", accounts, calls, err)
	}
	if accounts[0].ObservedAt == "" || !strings.Contains(accounts[0].Message, "unknown") || accounts[0].Credits != nil {
		t.Fatalf("unknown=%#v", accounts[0])
	}
	encoded, err := json.Marshal(accounts)
	if err != nil || strings.Contains(string(encoded), "secret-token") {
		t.Fatal("raw status_message leaked")
	}
	codex := accounts[5]
	if codex.Credits != nil || codex.ObservedAt != "" || len(codex.Windows) != 2 || !strings.Contains(codex.Windows[0].Name, `model "model-a"`) || !strings.Contains(codex.Windows[1].Name, "2026-09-07T11:00:00Z") {
		t.Fatalf("model isolation lost: %#v", codex)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
