package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	grokconfig "grok_switch/internal/config"
	"grok_switch/internal/modelvariants"
	"grok_switch/internal/profiles"
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
		{ID: "subscription/codex/gpt-sub", Provider: "codex", Label: "gpt-sub"},
		{ID: "subscription/codex/gpt-unselected", Provider: "codex", Label: "gpt-unselected"},
		{ID: "subscription/gemini/gemini-sub", Provider: "gemini", Label: "gemini-sub"},
		{ID: "subscription/grok/grok-sub", Provider: "grok", Label: "grok-sub"},
	}, nil
}

func TestSubscriptionProfileGeneratesOnlyExactTrustedStandardFastPairs(t *testing.T) {
	accounts := []SubscriptionProxyAccount{
		{ID: "codex", Provider: "codex"},
		{ID: "gemini", Provider: "gemini"},
	}
	baseURL := "http://127.0.0.1:17878/subscription-proxy/v1"
	profile := subscriptionProfile("codex", "Codex", "secret", accounts, []SubscriptionProxyModel{
		{ID: "subscription/codex/gpt-5.6-terra", Provider: "codex", Label: "gpt-5.6-terra"},
		{ID: "subscription/codex/gpt-5.6-unknown", Provider: "codex", Label: "gpt-5.6-unknown"},
		{ID: "subscription/codex/gpt-5.6-unknown-fast", Provider: "codex", Label: "gpt-5.6-unknown-fast"},
		{ID: "subscription/codex/gpt-5.6-sol-fast", Provider: "codex", Label: "gpt-5.6-sol-fast"},
		{ID: "subscription/gemini/gpt-5.6-luna", Provider: "gemini", Label: "gpt-5.6-luna"},
	}, baseURL)

	standard := "subscription/codex/gpt-5.6-terra"
	fast := standard + "-fast"
	if profile.DefaultModel != standard || profile.DefaultReasoningEffort != "low" {
		t.Fatalf("trusted default = %q/%q, want %q/low", profile.DefaultModel, profile.DefaultReasoningEffort, standard)
	}
	if len(profile.Models) != 4 {
		t.Fatalf("models = %#v, want trusted pair plus two unclassified codex selections", profile.Models)
	}
	byName := map[string]profiles.ModelDef{}
	for _, model := range profile.Models {
		byName[model.Name] = model
	}
	standardModel, ok := byName[standard]
	if !ok || standardModel.Model != standard || standardModel.SpeedTier != profiles.SpeedTierStandard || standardModel.StandardAnchor != standard {
		t.Fatalf("standard model = %#v", standardModel)
	}
	fastModel, ok := byName[fast]
	if !ok || fastModel.Model != fast || fastModel.SpeedTier != profiles.SpeedTierFast || fastModel.StandardAnchor != standard {
		t.Fatalf("fast model = %#v", fastModel)
	}
	for _, model := range []profiles.ModelDef{standardModel, fastModel} {
		if !model.SupportsReasoningEffort || model.ReasoningEffortsSource != "declared" || !reflect.DeepEqual(model.ReasoningEfforts, modelvariants.TrustedCodexReasoningEfforts()) {
			t.Fatalf("trusted model reasoning metadata = %#v", model)
		}
	}
	for _, name := range []string{"subscription/codex/gpt-5.6-unknown", "subscription/codex/gpt-5.6-unknown-fast"} {
		model, ok := byName[name]
		if !ok {
			t.Fatalf("unclassified selected model %q missing: %#v", name, profile.Models)
		}
		if model.SpeedTier != "" || model.StandardAnchor != "" || model.SupportsReasoningEffort || len(model.ReasoningEfforts) != 0 || model.ReasoningEffortsSource != "default" {
			t.Fatalf("untrusted model %q was classified: %#v", name, model)
		}
	}
	if _, ok := byName["subscription/codex/gpt-5.6-sol-fast"]; ok {
		t.Fatal("generated trusted Fast alias was accepted as a selectable physical model")
	}
	if _, ok := byName["subscription/gemini/gpt-5.6-luna"]; ok {
		t.Fatal("different provider model leaked into codex profile")
	}
	if err := profiles.ValidateModelVariants(profile); err != nil {
		t.Fatalf("generated profile has invalid variants: %v", err)
	}
}

func TestFindSubscriptionProfilePrefersOwnedIdentity(t *testing.T) {
	baseURL := "http://127.0.0.1:17878/subscription-proxy/v1"
	list := []profiles.Profile{
		{Name: "订阅代理 · ChatGPT/Codex", BaseURL: baseURL, Models: []profiles.ModelDef{{Name: "subscription/codex/legacy"}}},
		{ID: "owned", Name: "订阅代理 · ChatGPT/Codex", Source: "subscription-proxy:codex", BaseURL: baseURL, Models: []profiles.ModelDef{{Name: "subscription/codex/current"}}},
	}
	got, err := findSubscriptionProfile(list, "codex", "订阅代理 · ChatGPT/Codex", baseURL)
	if err != nil || got == nil || got.ID != "owned" {
		t.Fatalf("profile = %#v, err = %v; want owned identity", got, err)
	}
}

func TestFindSubscriptionProfileAdoptsOnlyExactUnambiguousLegacyIdentity(t *testing.T) {
	baseURL := "http://127.0.0.1:17878/subscription-proxy/v1"
	legacy := profiles.Profile{ID: "legacy", Name: "订阅代理 · ChatGPT/Codex", BaseURL: baseURL, Models: []profiles.ModelDef{
		{Name: "subscription/codex/gpt-5.6-sol"},
		{Name: "subscription/codex/gpt-5.6-terra"},
	}}
	got, err := findSubscriptionProfile([]profiles.Profile{legacy}, "codex", legacy.Name, baseURL)
	if err != nil || got == nil || got.ID != legacy.ID {
		t.Fatalf("profile = %#v, err = %v; want exact legacy identity", got, err)
	}

	ordinary := legacy
	ordinary.ID = "ordinary"
	ordinary.Name = "My Codex gateway"
	got, err = findSubscriptionProfile([]profiles.Profile{ordinary}, "codex", legacy.Name, baseURL)
	if err != nil || got != nil {
		t.Fatalf("ordinary profile was claimed: profile=%#v err=%v", got, err)
	}

	otherProvider := legacy
	otherProvider.ID = "other-provider"
	otherProvider.Models = []profiles.ModelDef{{Name: "subscription/gemini/model"}}
	got, err = findSubscriptionProfile([]profiles.Profile{otherProvider}, "codex", legacy.Name, baseURL)
	if err != nil || got != nil {
		t.Fatalf("other-provider profile was claimed: profile=%#v err=%v", got, err)
	}
}

func TestFindSubscriptionProfileRejectsAmbiguousLegacyIdentities(t *testing.T) {
	baseURL := "http://127.0.0.1:17878/subscription-proxy/v1"
	legacy := profiles.Profile{Name: "订阅代理 · ChatGPT/Codex", BaseURL: baseURL, Models: []profiles.ModelDef{{Name: "subscription/codex/model"}}}
	first, second := legacy, legacy
	first.ID, second.ID = "first", "second"
	got, err := findSubscriptionProfile([]profiles.Profile{first, second}, "codex", legacy.Name, baseURL)
	if err == nil || got != nil || !strings.Contains(err.Error(), "多个未标记") {
		t.Fatalf("profile=%#v err=%v; want ambiguity failure", got, err)
	}
}

func TestSubscriptionProfileLeavesUntrustedProviderWithoutReasoningDefault(t *testing.T) {
	profile := subscriptionProfile("gemini", "Gemini", "secret", []SubscriptionProxyAccount{{ID: "gemini", Provider: "gemini"}}, []SubscriptionProxyModel{{
		ID: "subscription/gemini/gpt-5.6-terra", Provider: "gemini", Label: "gpt-5.6-terra",
	}}, "http://127.0.0.1:17878/subscription-proxy/v1")
	if profile.DefaultModel != "subscription/gemini/gpt-5.6-terra" || profile.DefaultReasoningEffort != "none" || len(profile.Models) != 1 {
		t.Fatalf("untrusted provider defaults = %#v", profile)
	}
	if profile.Models[0].SpeedTier != "" || profile.Models[0].StandardAnchor != "" || profile.Models[0].SupportsReasoningEffort {
		t.Fatalf("gemini lookalike received trusted metadata: %#v", profile.Models[0])
	}
}

func TestRollbackSubscriptionProfilesRestoresExactState(t *testing.T) {
	store := profiles.NewStore(t.TempDir() + "/profiles.json")
	previous, err := store.Create(profiles.Profile{
		Name:         "Previous",
		Source:       "subscription-proxy:codex",
		BaseURL:      "http://127.0.0.1:17878/subscription-proxy/v1",
		DefaultModel: "subscription/codex/gpt-sub",
		Models:       []profiles.ModelDef{{Name: "subscription/codex/gpt-sub", Model: "subscription/codex/gpt-sub"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated := previous
	updated.Name = "Updated"
	if _, err := store.Update(previous.ID, updated); err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(profiles.Profile{Name: "Created", Source: "manual"})
	if err != nil {
		t.Fatal(err)
	}

	if err := rollbackSubscriptionProfiles(store, []string{created.ID}, []profiles.Profile{previous}); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal([]profiles.Profile{previous})
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("profiles after rollback = %s, want exact previous %s", gotJSON, wantJSON)
	}
}

func TestRollbackSubscriptionProfilesReportsEveryFailure(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/profiles.json"
	store := profiles.NewStore(path)
	previous := profiles.Profile{ID: "previous", Name: "Previous", Source: "manual"}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}

	err := rollbackSubscriptionProfiles(store, []string{"created"}, []profiles.Profile{previous})
	if err == nil || !strings.Contains(err.Error(), `删除新建订阅配置 "created" 失败`) || !strings.Contains(err.Error(), `恢复订阅配置 "previous" 失败`) {
		t.Fatalf("rollback error = %v, want both failures", err)
	}
	combined := transactionRollbackError(os.ErrPermission, err)
	if !errors.Is(combined, os.ErrPermission) || !errors.Is(combined, err) || !strings.Contains(combined.Error(), "回滚未完整完成") {
		t.Fatalf("combined transaction error = %v", combined)
	}
}

func TestSubscriptionProviderCreationRefreshesCombinedRouting(t *testing.T) {
	s := newRoutingTestServer(t)
	s.SubscriptionProxy = &subscriptionProfileRoutingFake{}
	s.ActualPort = 17878
	s.subscriptionProxyState = &subscriptionProxySelection{
		selected: map[string]bool{"codex\x00subscription/codex/gpt-sub": true},
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
	if len(subscriptionModels) != 1 || subscriptionModels[0] != "subscription/codex/gpt-sub" {
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
	for _, model := range []string{"upstream-one", "upstream-two", "subscription/codex/gpt-sub"} {
		if !strings.Contains(string(config), model) {
			t.Fatalf("combined config missing %q: %s", model, config)
		}
	}
	for _, model := range []string{"subscription/codex/gpt-unselected", "subscription/gemini/gemini-sub", "subscription/grok/grok-sub"} {
		if strings.Contains(string(config), model) {
			t.Fatalf("combined config unexpectedly contains unselected model %q: %s", model, config)
		}
	}
}
