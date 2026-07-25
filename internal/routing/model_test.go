package routing

import (
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

// TestSubagentProviderBackendMismatch 验证跨供应商子代理的工具适配。
// 当子代理指向与主对话不同后端的供应商时，工具 schema 应该被正确过滤。
func TestSubagentProviderBackendMismatch(t *testing.T) {
	source := []profiles.Profile{
		{
			ID: "p1", Name: "Responses", DefaultModel: "main",
			Models: []profiles.ModelDef{
				{Name: "main", Model: "main", APIBackend: "responses", SupportsBackendSearch: true},
			},
		},
		{
			ID: "p2", Name: "ChatCompletions", DefaultModel: "sub",
			Models: []profiles.ModelDef{
				{Name: "sub", Model: "sub", APIBackend: "chat_completions", SupportsBackendSearch: false},
			},
		},
	}
	// 主对话使用 responses 后端（支持 x_search），子代理指向 chat_completions 后端
	policy := RoutingPolicy{
		Default:   "main",
		WebSearch: "main",
		Subagents: SubagentsPolicy{Explore: "sub", Plan: "sub"},
	}
	snapshot, err := ProjectWithPolicy(source, policy)
	if err != nil {
		t.Fatal(err)
	}

	// 验证主对话的 web_search 能力（responses + backend_search = true）
	if !snapshot.WebSearchCapable() {
		t.Fatal("main conversation should be web_search capable (responses + backend_search)")
	}

	// 验证子代理的 web_search 能力：chat_completions 后端不应该支持 x_search
	if snapshot.SubagentWebSearchCapable("explore") {
		t.Fatal("explore subagent should NOT be web_search capable (chat_completions backend)")
	}
	if snapshot.SubagentWebSearchCapable("plan") {
		t.Fatal("plan subagent should NOT be web_search capable (chat_completions backend)")
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
