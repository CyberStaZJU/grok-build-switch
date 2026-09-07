package server

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func quotaTestAccount(id, creditType string, remaining float64) SubscriptionQuotaAccount {
	return SubscriptionQuotaAccount{ID: id, Provider: "codex", Status: "active", Credits: &SubscriptionQuotaCredits{Type: creditType, Remaining: remaining}}
}

func TestAggregateSubscriptionQuotasReadOnlyFilteredDeduplicated(t *testing.T) {
	input := []SubscriptionQuotaAccount{
		{ID: "google", Provider: "antigravity", Email: "z", Status: "active"},
		quotaTestAccount("same", "credits", 12),
		quotaTestAccount("same", "credits", 12),
		{ID: "other", Provider: "claude", Status: "active"},
	}
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	report := aggregateSubscriptionQuotas(input, time.Unix(0, 0))
	after, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("aggregation mutated input")
	}
	if len(report.Accounts) != 2 || len(report.Providers) != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Accounts[0].Provider != "codex" || report.Accounts[1].Provider != "gemini" {
		t.Fatalf("unfiltered accounts: %+v", report.Accounts)
	}
	group := report.Providers[0]
	if group.AccountCount != 1 || group.AvailableAccounts != 1 || group.CreditsRemaining == nil || *group.CreditsRemaining != 12 {
		t.Fatalf("duplicate counted: %+v", group)
	}
}

func TestAggregateSubscriptionQuotasAvailabilityAndPartial(t *testing.T) {
	input := []SubscriptionQuotaAccount{quotaTestAccount("active", "credits", 10)}
	for _, status := range []string{"disabled", "error", "unknown", "", "pending"} {
		account := quotaTestAccount("status-"+status, "credits", 100)
		account.Status = status
		input = append(input, account)
	}
	disabled := quotaTestAccount("disabled-flag", "credits", 100)
	disabled.Disabled = true
	unavailable := quotaTestAccount("unavailable", "credits", 100)
	unavailable.Unavailable = true
	missing := quotaTestAccount("missing", "credits", 100)
	missing.Credits = nil
	input = append(input, disabled, unavailable, missing)
	group := aggregateSubscriptionQuotas(input, time.Now()).Providers[0]
	if group.AccountCount != 9 || group.AvailableAccounts != 2 || group.DisabledAccounts != 2 || group.ErrorAccounts != 2 {
		t.Fatalf("wrong counts: %+v", group)
	}
	if group.CreditsRemaining == nil || *group.CreditsRemaining != 10 || !strings.Contains(group.Message, "已知部分") || !strings.Contains(group.Message, "1/9") {
		t.Fatalf("partial balance not explicit: %+v", group)
	}
}

func TestAggregateSubscriptionQuotasMixedCreditsStayBlocked(t *testing.T) {
	for _, types := range [][]string{{"A", "B", "A"}, {"A", "B", "B"}, {"B", "A", "B"}} {
		var input []SubscriptionQuotaAccount
		for i, kind := range types {
			input = append(input, quotaTestAccount(string(rune('a'+i)), kind, 10))
		}
		group := aggregateSubscriptionQuotas(input, time.Now()).Providers[0]
		if group.CreditsRemaining != nil || group.CreditType != "" || !strings.Contains(group.Message, "不能合计") {
			t.Fatalf("mixed types %v restored total: %+v", types, group)
		}
	}
}

func TestAggregateSubscriptionQuotasRejectInvalidCredits(t *testing.T) {
	for _, value := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, field := range []string{"remaining", "minimum"} {
			account := quotaTestAccount("invalid", "credits", 1)
			if field == "remaining" {
				account.Credits.Remaining = value
			} else {
				account.Credits.MinimumPerUse = value
			}
			report := aggregateSubscriptionQuotas([]SubscriptionQuotaAccount{account}, time.Now())
			if report.Accounts[0].Credits != nil || report.Providers[0].CreditsRemaining != nil {
				t.Fatalf("accepted invalid %s %v", field, value)
			}
			if _, err := json.Marshal(report); err != nil {
				t.Fatalf("report cannot be encoded: %v", err)
			}
			if account.Credits == nil {
				t.Fatal("input credits removed")
			}
		}
	}
	account := quotaTestAccount("empty-type", " ", 10)
	if group := aggregateSubscriptionQuotas([]SubscriptionQuotaAccount{account}, time.Now()).Providers[0]; group.CreditsRemaining != nil {
		t.Fatal("accepted unknown credit type")
	}
	input := []SubscriptionQuotaAccount{quotaTestAccount("a", "credits", math.MaxFloat64), quotaTestAccount("b", "credits", math.MaxFloat64), quotaTestAccount("c", "credits", 1)}
	if group := aggregateSubscriptionQuotas(input, time.Now()).Providers[0]; group.CreditsRemaining != nil {
		t.Fatal("overflow restored total")
	}
}

func TestAggregateSubscriptionQuotasObservationNotExpiry(t *testing.T) {
	old := quotaTestAccount("old", "credits", 0)
	old.ObservedAt = "2000-01-01T00:00:00Z"
	fresh := quotaTestAccount("fresh", "credits", 20)
	fresh.ObservedAt = "2026-09-07T00:00:00Z"
	group := aggregateSubscriptionQuotas([]SubscriptionQuotaAccount{fresh, old}, time.Now()).Providers[0]
	if group.CreditsRemaining == nil || *group.CreditsRemaining != 20 || group.AvailableAccounts != 2 || group.ObservedAt != old.ObservedAt || group.Message != "" {
		t.Fatalf("unexpected expiration or zero handling: %+v", group)
	}
}

func TestAggregateSubscriptionQuotasEmptyAndUnknown(t *testing.T) {
	report := aggregateSubscriptionQuotas(nil, time.Unix(0, 0).UTC())
	if report.Accounts == nil || !reflect.DeepEqual(report.Providers, []SubscriptionQuotaProvider{{Provider: "codex", Message: "尚未添加账号"}, {Provider: "gemini", Message: "尚未添加账号"}}) {
		t.Fatalf("unexpected empty report: %+v", report)
	}
	account := quotaTestAccount("percent-only", "credits", 0)
	account.Credits = nil
	used := 50.0
	account.Windows = []SubscriptionQuotaWindow{{Name: "primary", UsedPercent: &used}}
	group := aggregateSubscriptionQuotas([]SubscriptionQuotaAccount{account}, time.Now()).Providers[0]
	if group.CreditsRemaining != nil || !strings.Contains(group.Message, "未知") || !strings.Contains(group.Message, "百分比额度不合计") {
		t.Fatalf("invented absolute balance: %+v", group)
	}
}
