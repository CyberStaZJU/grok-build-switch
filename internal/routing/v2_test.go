package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"grok_switch/internal/profiles"
)

func TestSchemaV1MigrationChoosesDefaultProviderAndRemembersLocalPolicies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.json")
	legacy := map[string]any{
		"version": 1,
		"providers": []map[string]any{
			{"id": "one", "name": "One", "profile_id": "one"},
			{"id": "two", "name": "Two", "profile_id": "two"},
		},
		"model_routes": []map[string]any{
			{"id": "one:a", "name": "a", "provider_id": "one", "profile_model": "a"},
			{"id": "two:b", "name": "b", "provider_id": "two", "profile_model": "b"},
		},
		"policy": map[string]any{"default": "b", "web_search": "a", "subagents": map[string]any{"explore": "a", "plan": "b"}},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewStore(path).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != CurrentVersion || snapshot.ActiveProviderID != "two" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.ProviderPolicies["two"].Default != "two:b" || snapshot.ProviderPolicies["two"].Subagents.Plan != "two:b" {
		t.Fatalf("active policy = %#v", snapshot.ProviderPolicies["two"])
	}
	if snapshot.ProviderPolicies["two"].WebSearch != "" || snapshot.ProviderPolicies["two"].Subagents.Explore != "" {
		t.Fatalf("cross-provider routes migrated: %#v", snapshot.ProviderPolicies["two"])
	}
	if snapshot.ProviderPolicies["one"].Default != "one:a" {
		t.Fatalf("provider one policy = %#v", snapshot.ProviderPolicies["one"])
	}
}

func TestProjectWithSnapshotRemembersEachProviderPolicy(t *testing.T) {
	items := []profiles.Profile{
		{ID: "one", Name: "One", DefaultModel: "a", Models: []profiles.ModelDef{{Name: "a", Model: "a"}, {Name: "a2", Model: "a2"}}},
		{ID: "two", Name: "Two", DefaultModel: "b", Models: []profiles.ModelDef{{Name: "b", Model: "b"}, {Name: "b2", Model: "b2"}}},
	}
	base := Project(items)
	base.ProviderPolicies["one"] = RoutingPolicy{Default: "one:a2", WebSearch: "one:a"}
	base.ProviderPolicies["two"] = RoutingPolicy{Default: "two:b2", Subagents: SubagentsPolicy{Plan: "two:b"}}
	base.ActiveProviderID = "two"
	base.Policy = RoutingPolicy{}
	projected, err := ProjectWithSnapshot(items, base)
	if err != nil {
		t.Fatal(err)
	}
	if projected.ActiveProviderID != "two" || projected.ProviderPolicies["one"].Default != "one:a2" || projected.ProviderPolicies["two"].Default != "two:b2" {
		t.Fatalf("projected = %#v", projected)
	}
}
