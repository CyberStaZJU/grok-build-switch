package server

import (
	"os/exec"

	acp "github.com/coder/acp-go-sdk"

	"grok_switch/internal/browseruse"
	"grok_switch/internal/routing"
)

// browserUseMCPName is the MCP server name injected for non-Grok subagents.
const browserUseMCPName = "browser-use"

var browserUseLookPath = exec.LookPath

// BrowserUseCommand resolves the browser-use MCP server executable.
// It prefers a bundled binary named "browser-use-mcp" on PATH, then falls
// back to "browser-use" if available.
func BrowserUseCommand() (string, []string, bool) {
	for _, name := range []string{"browser-use-mcp", "browser-use"} {
		if path, err := browserUseLookPath(name); err == nil {
			return path, nil, true
		}
	}
	return "", nil, false
}

// McpServersForSubagent returns the MCP servers to inject for a given
// subagent type. When the subagent's target model is a Grok series model
// (supports native x_search), no MCP servers are injected. For all other
// models, a browser-use MCP server is injected to provide web_search and
// web_fetch tools.
func McpServersForSubagent(snapshot routing.Snapshot, subagent string) []acp.McpServer {
	if snapshot.Policy.Official {
		// Official Grok models support x_search natively.
		return nil
	}
	target := subagentTarget(snapshot, subagent)
	if target == "" {
		// No subagent route configured; nothing to adapt.
		return nil
	}
	route, ok := snapshot.Route(target)
	if !ok {
		// Route missing from catalog; skip injection rather than guess.
		return nil
	}
	if route.APIBackend == "responses" && route.SupportsBackendSearch {
		return nil
	}
	return browserUseMCPServers()
}

// McpServersForMain returns the MCP servers to inject for the main session.
// When the main conversation uses a non-Grok model and web_search is
// configured, inject browser-use to provide search capability.
func McpServersForMain(snapshot routing.Snapshot) []acp.McpServer {
	if snapshot.Policy.Official {
		return nil
	}
	if !snapshot.WebSearchCapable() && snapshot.Policy.WebSearch != "" {
		// Main conversation web_search points at a non-x_search model.
		return browserUseMCPServers()
	}
	// Also check the default model using actual backend capability rather than
	// its marketing name. A grok-* alias on chat_completions has no x_search.
	if route, ok := snapshot.Route(snapshot.Policy.Default); ok {
		if route.APIBackend != "responses" || !route.SupportsBackendSearch {
			return browserUseMCPServers()
		}
	}
	return nil
}

// subagentTarget resolves the route name a subagent type points at.
func subagentTarget(snapshot routing.Snapshot, subagent string) string {
	switch subagent {
	case "explore":
		return snapshot.Policy.Subagents.Explore
	case "plan":
		return snapshot.Policy.Subagents.Plan
	}
	return ""
}

// browserUseMCPServers builds the MCP server list for browser-use fallback.
func browserUseMCPServers() []acp.McpServer {
	command, args, ok := BrowserUseCommand()
	if !ok {
		return nil
	}
	if len(args) == 0 {
		args = []string{"mcp", "serve"}
	}
	return []acp.McpServer{
		{
			Stdio: &acp.McpServerStdio{
				Name:    browserUseMCPName,
				Command: command,
				Args:    args,
				Env:     []acp.EnvVariable{},
			},
		},
	}
}

// ShouldInjectBrowserUse reports whether a model identifier requires
// browser-use MCP injection (i.e. is not a Grok series model).
func ShouldInjectBrowserUse(model string) bool {
	return !browseruse.IsGrokModel(model)
}
