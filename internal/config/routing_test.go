package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
)

func TestApplyRoutingComposesHydratedMultipleProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[ui]\nyolo = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profileList := []profiles.Profile{
		{
			ID:             "one",
			Name:           "One",
			UpstreamFormat: "openai_responses",
			BaseURL:        "https://one.example/v1",
			APIKey:         "key-one",
			DefaultModel:   "shared",
			Models: []profiles.ModelDef{{
				Name:                    "shared",
				Model:                   "upstream-one",
				SupportsReasoningEffort: true,
				ReasoningEfforts:        []string{"low", "high"},
				ExtraHeaders:            map[string]string{"X-Secret": "header-one"},
			}},
		},
		{
			ID:             "two",
			Name:           "Two",
			UpstreamFormat: "anthropic",
			BaseURL:        "https://two.example/v1",
			APIKey:         "key-two",
			Models:         []profiles.ModelDef{{Name: "shared", Model: "upstream-two", SupportsBackendSearch: true}},
		},
	}
	catalog := routing.Project(profileList)
	policy := routing.RoutingPolicy{
		Default:                "shared@One",
		DefaultReasoningEffort: "high",
		WebSearch:              "shared@Two",
		Subagents:              routing.SubagentsPolicy{Explore: "shared@Two", Plan: "shared@One"},
	}
	snapshot, err := routing.ProjectWithPolicy(profileList, policy)
	if err != nil {
		t.Fatal(err)
	}
	catalogJSON := mustJSON(t, catalog)
	for _, secret := range []string{"key-one", "key-two", "header-one", "X-Secret", "https://one.example/v1"} {
		if strings.Contains(catalogJSON, secret) {
			t.Fatalf("catalog JSON leaked %q: %s", secret, catalogJSON)
		}
	}
	if err := ApplyRoutingToFile(path, snapshot); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := toml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if tableAt(doc, "ui")["yolo"] != false {
		t.Fatalf("unrelated config lost: %s", data)
	}
	if got := stringAt(tableAt(doc, "models"), "default"); got != "shared@One" {
		t.Fatalf("default = %q", got)
	}
	if got := stringAt(tableAt(doc, "models"), "web_search"); got != "shared@Two" {
		t.Fatalf("web_search = %q", got)
	}
	models := tableAt(doc, "model")
	one, _ := models["shared@One"].(map[string]any)
	two, _ := models["shared@Two"].(map[string]any)
	if stringAt(one, "model") != "upstream-one" || stringAt(one, "base_url") != "https://one.example/v1" || stringAt(one, "api_key") != "key-one" || stringAt(one, "api_backend") != "responses" {
		t.Fatalf("provider one model = %#v", one)
	}
	if stringMapAt(one, "extra_headers")["X-Secret"] != "header-one" {
		t.Fatalf("provider one headers = %#v", one)
	}
	if stringAt(two, "model") != "upstream-two" || stringAt(two, "base_url") != "https://two.example/v1" || stringAt(two, "api_key") != "key-two" || stringAt(two, "api_backend") != "messages" {
		t.Fatalf("provider two model = %#v", two)
	}
	if got := stringAt(tableAt(tableAt(doc, "subagents"), "models"), "explore"); got != "shared@Two" {
		t.Fatalf("subagents explore = %q", got)
	}
	matched, err := CurrentMatchesRouting(path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("combined config should match hydrated snapshot after apply")
	}
}

func TestCurrentMatchesRoutingIgnoresSyntheticProfileKeyOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// Provider order intentionally differs from the alphabetical route order.
	// The projected profile derives its aggregate APIKey from z-route, while
	// TOML import reads a-route first. Only per-model keys are meaningful here.
	profileList := []profiles.Profile{
		{
			ID: "z-provider", Name: "Z", BaseURL: "https://z.example/v1", APIKey: "key-z",
			DefaultModel: "z-route", Models: []profiles.ModelDef{{Name: "z-route", Model: "upstream-z"}},
		},
		{
			ID: "a-provider", Name: "A", BaseURL: "https://a.example/v1", APIKey: "key-a",
			DefaultModel: "a-route", Models: []profiles.ModelDef{{Name: "a-route", Model: "upstream-a"}},
		},
	}
	policy := routing.RoutingPolicy{Default: "z-route", DefaultReasoningEffort: "medium"}
	snapshot, err := routing.ProjectWithPolicy(profileList, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyRoutingToFile(path, snapshot); err != nil {
		t.Fatal(err)
	}
	matched, err := CurrentMatchesRouting(path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("unchanged combined config should not depend on synthetic profile key order")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "key-a", "changed-key", 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	matched, err = CurrentMatchesRouting(path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("per-model API key drift must still be detected")
	}
}

func TestRoutingPreviewSnippetAndMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	profileList := []profiles.Profile{{
		ID:           "p",
		Name:         "P",
		BaseURL:      "https://p.example/v1",
		APIKey:       "secret",
		DefaultModel: "m",
		Models:       []profiles.ModelDef{{Name: "m", Model: "upstream-m", APIBackend: "chat_completions"}},
	}}
	snapshot, err := routing.ProjectWithPolicy(profileList, routing.Project(profileList).Policy)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewRouting(path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(preview), `[model.m]`) || !strings.Contains(string(preview), `api_key = 'secret'`) && !strings.Contains(string(preview), `api_key = "secret"`) {
		t.Fatalf("preview missing combined model:\n%s", preview)
	}
	snippet, err := SnippetForRouting(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snippet, "upstream-m") {
		t.Fatalf("snippet = %s", snippet)
	}
	if err := ApplyRoutingToFile(path, snapshot); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "upstream-m", "changed-upstream", 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	matched, err := CurrentMatchesRouting(path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("changed route should not match snapshot")
	}
}

func TestProfileForRoutingRejectsUnhydratedCatalog(t *testing.T) {
	profileList := []profiles.Profile{{ID: "p", Name: "P", DefaultModel: "m", Models: []profiles.ModelDef{{Name: "m", Model: "upstream-m"}}}}
	catalog := routing.Project(profileList)
	if _, err := ProfileForRouting(catalog); err == nil {
		t.Fatal("expected unhydrated routing catalog to be rejected")
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
