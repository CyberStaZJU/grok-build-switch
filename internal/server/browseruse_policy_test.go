package server

import (
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
)

func TestMcpServersForSubagent_GrokModel(t *testing.T) {
	// Grok model subagent should NOT inject browser-use.
	source := []profiles.Profile{
		{
			ID: "p1", Name: "Grok", DefaultModel: "main",
			Models: []profiles.ModelDef{
				{Name: "main", Model: "grok-4.5", APIBackend: "responses", SupportsBackendSearch: true},
			},
		},
		{
			ID: "p2", Name: "GrokExplore", DefaultModel: "explore",
			Models: []profiles.ModelDef{
				{Name: "explore", Model: "grok-4.5", APIBackend: "responses", SupportsBackendSearch: true},
			},
		},
	}
	policy := routing.RoutingPolicy{
		Default:   "main",
		Subagents: routing.SubagentsPolicy{Explore: "explore"},
	}
	snapshot, err := routing.ProjectWithPolicy(source, policy)
	if err != nil {
		t.Fatal(err)
	}
	servers := McpServersForSubagent(snapshot, "explore")
	if len(servers) != 0 {
		t.Fatalf("Grok model subagent should not inject browser-use, got %d servers", len(servers))
	}
}

func TestMcpServersForSubagent_NonGrokModel(t *testing.T) {
	// Non-Grok model subagent SHOULD inject browser-use.
	source := []profiles.Profile{
		{
			ID: "p1", Name: "Grok", DefaultModel: "main",
			Models: []profiles.ModelDef{
				{Name: "main", Model: "grok-4.5", APIBackend: "responses", SupportsBackendSearch: true},
			},
		},
		{
			ID: "p2", Name: "Anthropic", DefaultModel: "sub",
			Models: []profiles.ModelDef{
				{Name: "sub", Model: "claude-4-sonnet", APIBackend: "messages", SupportsBackendSearch: false},
			},
		},
	}
	policy := routing.RoutingPolicy{
		Default:   "main",
		Subagents: routing.SubagentsPolicy{Explore: "sub"},
	}
	snapshot, err := routing.ProjectWithPolicy(source, policy)
	if err != nil {
		t.Fatal(err)
	}
	servers := McpServersForSubagent(snapshot, "explore")
	if len(servers) != 1 {
		t.Fatalf("Non-Grok model subagent should inject browser-use, got %d servers", len(servers))
	}
	if servers[0].Stdio == nil {
		t.Fatal("Expected stdio MCP server")
	}
	if servers[0].Stdio.Name != "browser-use" {
		t.Fatalf("Expected browser-use MCP server, got %q", servers[0].Stdio.Name)
	}
}

func TestMcpServersForSubagent_OfficialMode(t *testing.T) {
	// Official mode should NOT inject browser-use.
	snapshot := routing.Snapshot{
		Policy: routing.RoutingPolicy{Official: true},
	}
	servers := McpServersForSubagent(snapshot, "explore")
	if len(servers) != 0 {
		t.Fatalf("Official mode should not inject browser-use, got %d servers", len(servers))
	}
}

func TestMcpServersForSubagent_NoRoute(t *testing.T) {
	// Subagent with no route configured should NOT inject browser-use.
	source := []profiles.Profile{
		{
			ID: "p1", Name: "Grok", DefaultModel: "main",
			Models: []profiles.ModelDef{
				{Name: "main", Model: "grok-4.5", APIBackend: "responses"},
			},
		},
	}
	policy := routing.RoutingPolicy{Default: "main"}
	snapshot, err := routing.ProjectWithPolicy(source, policy)
	if err != nil {
		t.Fatal(err)
	}
	servers := McpServersForSubagent(snapshot, "explore")
	if len(servers) != 0 {
		t.Fatalf("Subagent with no route should not inject browser-use, got %d servers", len(servers))
	}
}

func TestMcpServersForMain_NonGrokWebSearch(t *testing.T) {
	// Main conversation with non-Grok web_search SHOULD inject browser-use.
	source := []profiles.Profile{
		{
			ID: "p1", Name: "Anthropic", DefaultModel: "main",
			Models: []profiles.ModelDef{
				{Name: "main", Model: "claude-4-sonnet", APIBackend: "messages", SupportsBackendSearch: false},
			},
		},
	}
	policy := routing.RoutingPolicy{
		Default:   "main",
		WebSearch: "main",
	}
	snapshot, err := routing.ProjectWithPolicy(source, policy)
	if err != nil {
		t.Fatal(err)
	}
	servers := McpServersForMain(snapshot)
	if len(servers) != 1 {
		t.Fatalf("Non-Grok web_search main should inject browser-use, got %d servers", len(servers))
	}
}

func TestMcpServersForMain_GrokDefault(t *testing.T) {
	// Main conversation with Grok default model should NOT inject browser-use.
	source := []profiles.Profile{
		{
			ID: "p1", Name: "Grok", DefaultModel: "main",
			Models: []profiles.ModelDef{
				{Name: "main", Model: "grok-4.5", APIBackend: "responses", SupportsBackendSearch: true},
			},
		},
	}
	policy := routing.RoutingPolicy{Default: "main"}
	snapshot, err := routing.ProjectWithPolicy(source, policy)
	if err != nil {
		t.Fatal(err)
	}
	servers := McpServersForMain(snapshot)
	if len(servers) != 0 {
		t.Fatalf("Grok default main should not inject browser-use, got %d servers", len(servers))
	}
}

func TestDedupeMCPServers(t *testing.T) {
	servers := []acp.McpServer{
		{Stdio: &acp.McpServerStdio{Name: "browser-use", Command: "/path/a"}},
		{Stdio: &acp.McpServerStdio{Name: "browser-use", Command: "/path/b"}},
		{Stdio: &acp.McpServerStdio{Name: "other", Command: "/path/c"}},
	}
	deduped := dedupeMCPServers(servers)
	if len(deduped) != 2 {
		t.Fatalf("Expected 2 deduped servers, got %d", len(deduped))
	}
	if deduped[0].Stdio.Name != "browser-use" || deduped[1].Stdio.Name != "other" {
		t.Fatalf("Unexpected deduped server order: %s, %s", deduped[0].Stdio.Name, deduped[1].Stdio.Name)
	}
}
