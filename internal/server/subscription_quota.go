package server

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

type SubscriptionQuotaWindow struct {
	Name          string   `json:"name"`
	UsedPercent   *float64 `json:"used_percent,omitempty"`
	WindowMinutes *int64   `json:"window_minutes,omitempty"`
	ResetAt       string   `json:"reset_at,omitempty"`
	ResetAfter    *int64   `json:"reset_after_seconds,omitempty"`
	LimitReached  *bool    `json:"limit_reached,omitempty"`
}

type SubscriptionQuotaCredits struct {
	Type          string  `json:"type"`
	Remaining     float64 `json:"remaining"`
	MinimumPerUse float64 `json:"minimum_per_use,omitempty"`
	Tier          string  `json:"tier,omitempty"`
}

type SubscriptionQuotaAccount struct {
	ID          string                    `json:"id"`
	Provider    string                    `json:"provider"`
	Label       string                    `json:"label,omitempty"`
	Email       string                    `json:"email,omitempty"`
	Plan        string                    `json:"plan,omitempty"`
	Status      string                    `json:"status"`
	Disabled    bool                      `json:"disabled"`
	Unavailable bool                      `json:"unavailable"`
	ObservedAt  string                    `json:"observed_at,omitempty"`
	Source      string                    `json:"source"`
	Windows     []SubscriptionQuotaWindow `json:"windows"`
	Credits     *SubscriptionQuotaCredits `json:"credits,omitempty"`
	NextRetryAt string                    `json:"next_retry_at,omitempty"`
	Message     string                    `json:"message,omitempty"`
}

type SubscriptionQuotaProvider struct {
	Provider          string   `json:"provider"`
	AccountCount      int      `json:"account_count"`
	AvailableAccounts int      `json:"available_accounts"`
	DisabledAccounts  int      `json:"disabled_accounts"`
	ErrorAccounts     int      `json:"error_accounts"`
	CreditType        string   `json:"credit_type,omitempty"`
	CreditsRemaining  *float64 `json:"credits_remaining,omitempty"`
	ObservedAt        string   `json:"observed_at,omitempty"`
	Message           string   `json:"message,omitempty"`
}

type SubscriptionQuotaReport struct {
	UpdatedAt string                      `json:"updated_at"`
	Providers []SubscriptionQuotaProvider `json:"providers"`
	Accounts  []SubscriptionQuotaAccount  `json:"accounts"`
}

type subscriptionQuotaReader interface {
	Quotas(context.Context) ([]SubscriptionQuotaAccount, error)
}

func (s *Server) handleSubscriptionProxyQuotas(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !subscriptionLoopback(w, r) || s.requireSubscriptionProxy(w) == nil {
		return
	}
	reader, ok := s.SubscriptionProxy.(subscriptionQuotaReader)
	if !ok {
		subscriptionProxyError(w, errSubscriptionProxyUnavailable, http.StatusServiceUnavailable)
		return
	}
	accounts, err := reader.Quotas(r.Context())
	if err != nil {
		subscriptionProxyError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, aggregateSubscriptionQuotas(accounts, time.Now()))
}

func aggregateSubscriptionQuotas(accounts []SubscriptionQuotaAccount, now time.Time) SubscriptionQuotaReport {
	groups := map[string]*SubscriptionQuotaProvider{}
	oldest := map[string]time.Time{}
	seen := map[string]bool{}
	blocked := map[string]bool{}
	known := map[string]int{}
	filtered := make([]SubscriptionQuotaAccount, 0, len(accounts))
	for _, account := range accounts {
		account.Provider = canonicalProvider(account.Provider)
		provider := account.Provider
		if provider != "codex" && provider != "gemini" {
			continue
		}
		if id := strings.TrimSpace(account.ID); id != "" {
			key := provider + "\x00" + id
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		if credits := account.Credits; credits != nil {
			if strings.TrimSpace(credits.Type) == "" || !validSubscriptionQuotaNumber(credits.Remaining) || !validSubscriptionQuotaNumber(credits.MinimumPerUse) {
				account.Credits = nil
				account.Message = strings.TrimSpace(account.Message + " 无效 credits 已拒绝，额度未知")
			}
		}
		filtered = append(filtered, account)
		group := groups[provider]
		if group == nil {
			group = &SubscriptionQuotaProvider{Provider: provider}
			groups[provider] = group
		}
		group.AccountCount++
		status := strings.ToLower(strings.TrimSpace(account.Status))
		available := !account.Disabled && !account.Unavailable && status == "active"
		if account.Disabled || status == "disabled" {
			group.DisabledAccounts++
		} else if account.Unavailable || status == "error" {
			group.ErrorAccounts++
		} else if available {
			group.AvailableAccounts++
		}
		if observed, err := time.Parse(time.RFC3339Nano, account.ObservedAt); err == nil {
			if oldest[provider].IsZero() || observed.Before(oldest[provider]) {
				oldest[provider] = observed
			}
		}
		if account.Credits != nil && available && !blocked[provider] {
			creditType := strings.TrimSpace(account.Credits.Type)
			if group.CreditType != "" && group.CreditType != creditType {
				blocked[provider] = true
				group.CreditType = ""
				group.CreditsRemaining = nil
				group.Message = "检测到多个不同类型的额度池，不能合计，请查看账号明细"
				continue
			}
			group.CreditType = creditType
			total := account.Credits.Remaining
			if group.CreditsRemaining != nil {
				total += *group.CreditsRemaining
			}
			if !validSubscriptionQuotaNumber(total) {
				blocked[provider] = true
				group.CreditType = ""
				group.CreditsRemaining = nil
				group.Message = "credits 合计超出有效数值范围，请查看账号明细"
				continue
			}
			group.CreditsRemaining = &total
			known[provider]++
		}
	}
	providers := make([]SubscriptionQuotaProvider, 0, 2)
	for _, provider := range []string{"codex", "gemini"} {
		group := groups[provider]
		if group == nil {
			group = &SubscriptionQuotaProvider{Provider: provider, Message: "尚未添加账号"}
		}
		if observed := oldest[provider]; !observed.IsZero() {
			group.ObservedAt = observed.Format(time.RFC3339)
		}
		if group.AccountCount > 0 && !blocked[provider] {
			if group.CreditsRemaining == nil {
				group.Message = "可用账号的绝对 credits 未知；百分比额度不合计，请查看账号明细"
			} else if known[provider] < group.AccountCount {
				group.Message = fmt.Sprintf("仅合计已知部分 credits（%d/%d 个账号）；停用、错误、状态未知或额度未知账号未计入，不代表完整余额", known[provider], group.AccountCount)
			}
		}
		providers = append(providers, *group)
	}
	accounts = filtered
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Provider != accounts[j].Provider {
			return accounts[i].Provider < accounts[j].Provider
		}
		return accounts[i].Email < accounts[j].Email
	})
	return SubscriptionQuotaReport{UpdatedAt: now.Format(time.RFC3339), Providers: providers, Accounts: accounts}
}

func validSubscriptionQuotaNumber(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
