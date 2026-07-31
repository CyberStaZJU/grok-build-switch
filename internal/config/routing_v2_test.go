package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
)

func TestCustomSwitchKeepsCombinedDefinitionsAndClearsOptionalKeys(t *testing.T) {
	items := []profiles.Profile{
		{ID: "one", Name: "One", DefaultModel: "a", BaseURL: "https://one.example/v1", APIKey: "one-key", Models: []profiles.ModelDef{{Name: "a", Model: "upstream-a"}}},
		{ID: "two", Name: "Two", DefaultModel: "b", BaseURL: "https://two.example/v1", APIKey: "two-key", Models: []profiles.ModelDef{{Name: "b", Model: "upstream-b"}}},
	}
	state := routing.Project(items)
	state.ActiveProviderID = "two"
	state.ProviderPolicies["two"] = routing.RoutingPolicy{Default: "two:b"}
	state.Policy = routing.RoutingPolicy{}
	snapshot, err := routing.ProjectWithSnapshot(items, state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[models]\nweb_search = \"stale\"\n[subagents.models]\nexplore = \"stale\"\nplan = \"stale\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyRoutingToFile(path, snapshot); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	for _, expected := range []string{"[model.a]", "[model.b]", `default = 'b'`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %s:\n%s", expected, text)
		}
	}
	for _, stale := range []string{`web_search = "stale"`, `explore = "stale"`, `plan = "stale"`} {
		if strings.Contains(text, stale) {
			t.Fatalf("stale managed key %s survived:\n%s", stale, text)
		}
	}
}

func TestOfficialSwitchRemovesCustomDefinitions(t *testing.T) {
	policy := routing.RoutingPolicy{Default: "grok-4.5"}
	input := []byte("[models]\ndefault = \"custom\"\n[model.custom]\nmodel = \"upstream\"\napi_key = \"secret\"\nbase_url = \"https://custom.example/v1\"\n")
	text := string(ApplyOfficialRoutingText(input, policy))
	if strings.Contains(text, "[model.custom]") || strings.Contains(text, "secret") || strings.Contains(text, "custom.example") {
		t.Fatalf("custom auth survived official switch:\n%s", text)
	}
	if !strings.Contains(text, `default = 'grok-4.5'`) {
		t.Fatalf("official default missing:\n%s", text)
	}
}
