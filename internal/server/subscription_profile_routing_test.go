package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	grokconfig "grok_switch/internal/config"
	"grok_switch/internal/routing"
)

type subscriptionProfileRoutingFake struct {
	subscriptionProxyServiceFake
}

func (*subscriptionProfileRoutingFake) Accounts(context.Context) ([]SubscriptionProxyAccount, error) {
	return []SubscriptionProxyAccount{
		{ID: "codex-account", Provider: "codex"},
		{ID: "gemini-account", Provider: "gemini"},
		{ID: "grok-account", Provider: "grok"},
	}, nil
}

func (*subscriptionProfileRoutingFake) Models(context.Context) ([]SubscriptionProxyModel, error) {
	return []SubscriptionProxyModel{
		{ID: "gpt-sub", Provider: "codex", Label: "subscription/codex/gpt-sub"},
		{ID: "gpt-unselected", Provider: "codex", Label: "subscription/codex/gpt-unselected"},
		{ID: "gemini-sub", Provider: "gemini", Label: "subscription/gemini/gemini-sub"},
		{ID: "grok-sub", Provider: "grok", Label: "subscription/grok/grok-sub"},
	}, nil
}

func TestSubscriptionProviderCreationRefreshesCombinedRouting(t *testing.T) {
	s := newRoutingTestServer(t)
	s.SubscriptionProxy = &subscriptionProfileRoutingFake{}
	s.ActualPort = 17878
	s.subscriptionProxyState = &subscriptionProxySelection{
		selected: map[string]bool{"codex\x00gpt-sub": true},
		sessions: map[string]string{},
	}

	request := loopbackRequest(http.MethodPost, "/api/subscription-proxy/providers", `{"provider":"codex"}`)
	response := httptest.NewRecorder()
	s.handleSubscriptionProxyProviders(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}

	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	profileList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	hydrated, err := routing.ProjectWithPolicy(profileList, stored.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(hydrated.Providers) != 3 {
		t.Fatalf("providers = %d, want 3", len(hydrated.Providers))
	}
	var subscriptionModels []string
	for _, profile := range profileList {
		if profile.Source != "subscription-proxy:codex" {
			continue
		}
		for _, model := range profile.Models {
			subscriptionModels = append(subscriptionModels, model.Model)
		}
	}
	if len(subscriptionModels) != 1 || subscriptionModels[0] != "gpt-sub" {
		t.Fatalf("subscription models = %v, want only selected gpt-sub", subscriptionModels)
	}
	matches, err := grokconfig.CurrentMatchesRouting(s.Paths.GrokConfig, hydrated)
	if err != nil || !matches {
		t.Fatalf("combined config matches=%v err=%v", matches, err)
	}
	config, err := os.ReadFile(s.Paths.GrokConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"upstream-one", "upstream-two", "gpt-sub"} {
		if !strings.Contains(string(config), model) {
			t.Fatalf("combined config missing %q: %s", model, config)
		}
	}
	for _, model := range []string{"gpt-unselected", "gemini-sub", "grok-sub"} {
		if strings.Contains(string(config), model) {
			t.Fatalf("combined config unexpectedly contains unselected model %q: %s", model, config)
		}
	}
}
