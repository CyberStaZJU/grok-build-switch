package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
		t.Fatalf("active provider absorbed cross-provider routes: %#v", snapshot.ProviderPolicies["two"])
	}
	if snapshot.ProviderPolicies["one"].Default != "one:a" || snapshot.ProviderPolicies["one"].WebSearch != "one:a" || snapshot.ProviderPolicies["one"].Subagents.Explore != "one:a" {
		t.Fatalf("provider one policy did not preserve its legacy selections: %#v", snapshot.ProviderPolicies["one"])
	}
}

func TestSchemaV1MigrationUsesDefaultOwnerEvenWhenOptionalRouteComesFirst(t *testing.T) {
	snapshot := migrateV1(
		[]Provider{{ID: "one"}, {ID: "two"}},
		[]ModelRoute{{ID: "one:a", Name: "a", ProviderID: "one"}, {ID: "two:b", Name: "b", ProviderID: "two"}},
		RoutingPolicy{Default: "missing", WebSearch: "two:b", Subagents: SubagentsPolicy{Explore: "one:a", Plan: "two:b"}},
		time.Time{},
	)
	if snapshot.ActiveProviderID != "one" {
		t.Fatalf("active provider = %q, want deterministic first provider when default is unavailable", snapshot.ActiveProviderID)
	}
	if snapshot.ProviderPolicies["two"].WebSearch != "two:b" || snapshot.ProviderPolicies["two"].Subagents.Plan != "two:b" {
		t.Fatalf("provider two optional memory = %#v", snapshot.ProviderPolicies["two"])
	}
	if snapshot.ProviderPolicies["one"].Subagents.Explore != "one:a" {
		t.Fatalf("provider one optional memory = %#v", snapshot.ProviderPolicies["one"])
	}
}

func TestPersistedEqualDetectsInactiveProviderPolicyRepairs(t *testing.T) {
	left := Snapshot{
		Version:          CurrentVersion,
		ActiveProviderID: "one",
		Providers:        []Provider{{ID: "one"}, {ID: "two"}},
		ModelRoutes:      []ModelRoute{{ID: "one:a", Name: "a", ProviderID: "one"}, {ID: "two:b", Name: "b", ProviderID: "two"}},
		ProviderPolicies: map[string]RoutingPolicy{"one": {Default: "one:a"}, "two": {Default: "two:b", WebSearch: "two:removed"}},
	}
	right := left
	right.ProviderPolicies = map[string]RoutingPolicy{"one": {Default: "one:a"}, "two": {Default: "two:b"}}
	if PersistedEqual(left, right) {
		t.Fatal("inactive provider policy change was ignored")
	}
	baseline := right
	baseline.Policy = RoutingPolicy{Default: "display-name"}
	baseline.UpdatedAt = time.Now()
	baseline.Hydrated = true
	if !PersistedEqual(baseline, right) {
		t.Fatal("runtime compatibility fields or timestamps affected persisted equality")
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
