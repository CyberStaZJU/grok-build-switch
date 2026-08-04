package routing

import (
	"strings"
	"testing"

	"grok_switch/internal/profiles"
)

// makeRoute 构造一个测试用 ModelRoute，Hydrated 字段已填充。
func makeRoute(name, providerID, model, apiBackend string, supportsSearch bool) ModelRoute {
	return ModelRoute{
		Name:                    name,
		ProviderID:              providerID,
		ProfileModel:            model,
		Model:                   model,
		APIBackend:              apiBackend,
		SupportsBackendSearch:   supportsSearch,
		SupportsReasoningEffort: true,
	}
}

func TestWebSearchCapableOfficial(t *testing.T) {
	s := Snapshot{Policy: RoutingPolicy{Official: true, WebSearch: "anything"}}
	if !s.WebSearchCapable() {
		t.Fatal("official mode should always be web_search capable")
	}
}

func TestEmptyRoutingSnapshotValidatesWithoutSyntheticEmptyProviderPolicy(t *testing.T) {
	snapshot := Project(nil)
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("empty projected snapshot invalid: %v", err)
	}
	projected, err := ProjectWithSnapshot(nil, snapshot)
	if err != nil {
		t.Fatalf("empty hydrated snapshot invalid: %v", err)
	}
	if _, ok := projected.ProviderPolicies[""]; ok {
		t.Fatal("empty projected snapshot contains synthetic empty-provider policy")
	}
}

func TestRoutingSnapshotRejectsExplicitEmptyProviderPolicy(t *testing.T) {
	snapshot := Project(nil)
	snapshot.ProviderPolicies[""] = RoutingPolicy{}
	if err := snapshot.Validate(); err == nil {
		t.Fatal("explicit empty-provider policy unexpectedly validated")
	}
}

func TestWebSearchCapableEmpty(t *testing.T) {
	s := Snapshot{Policy: RoutingPolicy{WebSearch: ""}}
	if !s.WebSearchCapable() {
		t.Fatal("empty web_search should be capable (no constraint)")
	}
}

func TestWebSearchCapableResponsesWithSearch(t *testing.T) {
	s := Snapshot{
		ModelRoutes: []ModelRoute{makeRoute("r1", "p1", "m1", "responses", true)},
		Policy:      RoutingPolicy{WebSearch: "r1"},
	}
	if !s.WebSearchCapable() {
		t.Fatal("responses + backend_search should be capable")
	}
}

func TestWebSearchCapableChatCompletions(t *testing.T) {
	s := Snapshot{
		ModelRoutes: []ModelRoute{makeRoute("r1", "p1", "m1", "chat_completions", true)},
		Policy:      RoutingPolicy{WebSearch: "r1"},
	}
	if s.WebSearchCapable() {
		t.Fatal("chat_completions backend should not be x_search capable")
	}
}

func TestWebSearchCapableResponsesWithoutSearchFlag(t *testing.T) {
	s := Snapshot{
		ModelRoutes: []ModelRoute{makeRoute("r1", "p1", "m1", "responses", false)},
		Policy:      RoutingPolicy{WebSearch: "r1"},
	}
	if s.WebSearchCapable() {
		t.Fatal("responses without supports_backend_search should not be capable")
	}
}

func TestWebSearchCapableMissingRoute(t *testing.T) {
	s := Snapshot{Policy: RoutingPolicy{WebSearch: "nonexistent"}}
	if s.WebSearchCapable() {
		t.Fatal("missing route should not be capable")
	}
}

func TestRepairWebSearchKeepsExisting(t *testing.T) {
	catalog := Snapshot{
		ModelRoutes: []ModelRoute{makeRoute("r1", "p1", "m1", "responses", true)},
	}
	if got := repairWebSearch("r1", catalog); got != "r1" {
		t.Fatalf("want r1, got %q", got)
	}
}

func TestRepairWebSearchClearsMissing(t *testing.T) {
	catalog := Snapshot{
		ModelRoutes: []ModelRoute{makeRoute("r1", "p1", "m1", "responses", true)},
	}
	if got := repairWebSearch("nonexistent", catalog); got != "" {
		t.Fatalf("want empty string for missing route, got %q", got)
	}
}

func TestRepairWebSearchKeepsChatCompletions(t *testing.T) {
	// repairWebSearch 不检查能力，只检查存在性。
	// 能力不足时由 WebSearchCapable=false 标记，而非擅自切换模型。
	catalog := Snapshot{
		ModelRoutes: []ModelRoute{makeRoute("r1", "p1", "m1", "chat_completions", false)},
	}
	if got := repairWebSearch("r1", catalog); got != "r1" {
		t.Fatalf("want r1 preserved, got %q", got)
	}
}

func TestRepairPolicyClearsInvalidWebSearch(t *testing.T) {
	source := []profiles.Profile{
		{
			ID: "active", Name: "Active", DefaultModel: "m1",
			Models: []profiles.ModelDef{
				{Name: "m1", Model: "m1", APIBackend: "responses", SupportsBackendSearch: true},
			},
		},
	}
	// Stale policy: web_search points at a nonexistent route.
	stale := RoutingPolicy{Default: "m1", WebSearch: "nonexistent"}
	repaired := RepairPolicy(source, stale)
	if repaired.WebSearch != "" {
		t.Fatalf("want web_search cleared, got %q", repaired.WebSearch)
	}
}

func TestProjectWithPolicyRejectsCrossProviderSubagents(t *testing.T) {
	source := []profiles.Profile{
		{ID: "p1", Name: "Responses", DefaultModel: "main", Models: []profiles.ModelDef{{Name: "main", Model: "main", APIBackend: "responses", SupportsBackendSearch: true}}},
		{ID: "p2", Name: "ChatCompletions", DefaultModel: "sub", Models: []profiles.ModelDef{{Name: "sub", Model: "sub", APIBackend: "chat_completions"}}},
	}
	snapshot, err := ProjectWithPolicy(source, RoutingPolicy{Default: "main", Subagents: SubagentsPolicy{Explore: "sub", Plan: "sub"}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ActivePolicy().Subagents.Explore != "" || snapshot.ActivePolicy().Subagents.Plan != "" {
		t.Fatalf("cross-provider subagent routes survived: %#v", snapshot.ActivePolicy())
	}
}

// TestSubagentWebSearchCapableWithResponses 验证子代理指向 responses 后端时支持 x_search。
func TestSubagentWebSearchCapableWithResponses(t *testing.T) {
	source := []profiles.Profile{
		{
			ID: "p1", Name: "ChatCompletions", DefaultModel: "main",
			Models: []profiles.ModelDef{
				{Name: "main", Model: "main", APIBackend: "chat_completions", SupportsBackendSearch: false},
			},
		},
		{
			ID: "p2", Name: "Responses", DefaultModel: "sub",
			Models: []profiles.ModelDef{
				{Name: "sub", Model: "sub", APIBackend: "responses", SupportsBackendSearch: true},
			},
		},
	}
	// 主对话使用 chat_completions 后端，web_search 也指向 chat_completions
	policy := RoutingPolicy{
		Default:   "main",
		WebSearch: "main",
		Subagents: SubagentsPolicy{Explore: "sub", Plan: "sub"},
	}
	snapshot, err := ProjectWithPolicy(source, policy)
	if err != nil {
		t.Fatal(err)
	}

	// 主对话不支持 x_search（chat_completions backend）
	if snapshot.WebSearchCapable() {
		t.Fatal("main conversation should NOT be web_search capable (chat_completions backend)")
	}

	// 子代理支持 x_search（responses + backend_search）
	if !snapshot.SubagentWebSearchCapable("explore") {
		t.Fatal("explore subagent should be web_search capable (responses backend)")
	}
	if !snapshot.SubagentWebSearchCapable("plan") {
		t.Fatal("plan subagent should be web_search capable (responses backend)")
	}
}

func TestProjectWithPolicySetsWebSearchCapable(t *testing.T) {
	source := []profiles.Profile{
		{
			ID: "p1", Name: "One", DefaultModel: "m1",
			Models: []profiles.ModelDef{
				{Name: "m1", Model: "m1", APIBackend: "chat_completions", SupportsBackendSearch: false},
			},
		},
		{
			ID: "p2", Name: "Two", DefaultModel: "m2",
			Models: []profiles.ModelDef{
				{Name: "m2", Model: "m2", APIBackend: "responses", SupportsBackendSearch: true},
			},
		},
	}
	snapshot, err := ProjectWithPolicy(source, RoutingPolicy{Default: "m1", WebSearch: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Policy.WebSearchCapable {
		t.Fatal("m1 is chat_completions; web_search should not be capable")
	}
	// Now point at a capable model.
	snapshot, err = ProjectWithPolicy(source, RoutingPolicy{Default: "m2", WebSearch: "m2"})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Policy.WebSearchCapable {
		t.Fatal("m2 is responses + backend_search; web_search should be capable")
	}
}

func TestProjectMapsExplicitSpeedRelationshipsWithoutChangingStandardIdentity(t *testing.T) {
	standard := "subscription/codex/gpt-5.6-terra"
	fast := standard + "-fast"
	snapshot := Project([]profiles.Profile{{
		ID: "codex", Name: "Codex", DefaultModel: standard,
		Models: []profiles.ModelDef{
			{Name: fast, Model: fast, SpeedTier: profiles.SpeedTierFast, StandardAnchor: standard},
			{Name: standard, Model: standard, SpeedTier: profiles.SpeedTierStandard, StandardAnchor: standard},
		},
	}})
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	standardID := "codex:" + standard
	fastID := "codex:" + fast
	standardRoute, ok := routeByExactID(snapshot, standardID)
	if !ok {
		t.Fatalf("standard route %q missing: %#v", standardID, snapshot.ModelRoutes)
	}
	if standardRoute.ID != standardID || standardRoute.Name != standard || standardRoute.ProfileModel != standard || standardRoute.SpeedTier != profiles.SpeedTierStandard || standardRoute.StandardAnchor != standardID {
		t.Fatalf("standard identity changed: %#v", standardRoute)
	}
	fastRoute, ok := routeByExactID(snapshot, fastID)
	if !ok || fastRoute.SpeedTier != profiles.SpeedTierFast || fastRoute.StandardAnchor != standardID {
		t.Fatalf("fast relationship = %#v", fastRoute)
	}
}

func TestSnapshotValidateRejectsForgedSpeedRelationships(t *testing.T) {
	base := Snapshot{
		Version: CurrentVersion, ActiveProviderID: "p1",
		Providers: []Provider{{ID: "p1", Name: "P1"}, {ID: "p2", Name: "P2"}},
		ModelRoutes: []ModelRoute{
			{ID: "p1:standard", Name: "standard", ProviderID: "p1", SpeedTier: profiles.SpeedTierStandard, StandardAnchor: "p1:standard"},
			{ID: "p1:fast", Name: "fast", ProviderID: "p1", SpeedTier: profiles.SpeedTierFast, StandardAnchor: "p1:standard"},
			{ID: "p2:standard", Name: "other", ProviderID: "p2", SpeedTier: profiles.SpeedTierStandard, StandardAnchor: "p2:standard"},
		},
		ProviderPolicies: map[string]RoutingPolicy{"p1": {Default: "p1:standard"}, "p2": {Default: "p2:standard"}},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid fixture: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Snapshot)
		want   string
	}{
		{name: "partial", mutate: func(s *Snapshot) { s.ModelRoutes[1].StandardAnchor = "" }, want: "both be present"},
		{name: "cross provider", mutate: func(s *Snapshot) { s.ModelRoutes[1].StandardAnchor = "p2:standard" }, want: "same provider"},
		{name: "missing", mutate: func(s *Snapshot) { s.ModelRoutes[1].StandardAnchor = "p1:missing" }, want: "unknown standard anchor"},
		{name: "anchor is fast", mutate: func(s *Snapshot) { s.ModelRoutes[1].StandardAnchor = "p1:fast" }, want: "standard route"},
		{name: "standard not self anchored", mutate: func(s *Snapshot) { s.ModelRoutes[0].StandardAnchor = "p1:fast" }, want: "self-anchor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			snapshot.ModelRoutes = append([]ModelRoute(nil), base.ModelRoutes...)
			test.mutate(&snapshot)
			err := snapshot.Validate()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}
