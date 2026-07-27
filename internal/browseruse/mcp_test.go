package browseruse

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsGrokModel(t *testing.T) {
	cases := map[string]bool{
		"grok-4.5":   true,
		"grok-4":     true,
		"grok":       true,
		"claude-4":   false,
		"gpt-5":      false,
		"":           false,
		"  grok-4  ": true, // trimmed
	}
	for model, want := range cases {
		if got := IsGrokModel(model); got != want {
			t.Errorf("IsGrokModel(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestMCPHandshakeAcceptsStandardInitializedNotification(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	server := &Server{stdin: strings.NewReader(input), stdout: &output, logger: New().logger}
	if err := server.Serve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "method not found") || !strings.Contains(output.String(), `"id":1`) || !strings.Contains(output.String(), `"id":2`) {
		t.Fatalf("unexpected MCP output: %s", output.String())
	}
}

func TestFindBrowserExecutableHonorsConfiguredPath(t *testing.T) {
	browser := filepath.Join(t.TempDir(), "Chrome")
	if err := os.WriteFile(browser, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROK_SWITCH_BROWSER_PATH", browser)
	got, err := findBrowserExecutable()
	if err != nil || got != browser {
		t.Fatalf("findBrowserExecutable() = %q, %v; want %q", got, err, browser)
	}
}

func TestParseDuckDuckGoResults(t *testing.T) {
	body := `<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa">Example &amp; Result</a>`
	results := parseDuckDuckGoResults(body, 5)
	if len(results) != 1 || results[0].Title != "Example & Result" || results[0].URL != "https://example.com/a" {
		t.Fatalf("results = %#v", results)
	}
}

func TestIsPublicAddressRejectsLocalAndReservedRanges(t *testing.T) {
	for _, raw := range []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "100.64.0.1", "192.0.2.1", "198.18.0.1",
		"198.51.100.1", "203.0.113.1", "224.0.0.1", "::1", "fe80::1", "fc00::1",
	} {
		if isPublicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("isPublicAddress(%s) = true", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !isPublicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("isPublicAddress(%s) = false", raw)
		}
	}
}

func TestValidatePublicURLRejectsLocalhost(t *testing.T) {
	for _, raw := range []string{"http://localhost:17878/api/config", "http://127.0.0.1/", "http://[::1]/"} {
		if err := validatePublicURL(context.Background(), raw); err == nil {
			t.Errorf("validatePublicURL(%q) succeeded", raw)
		}
	}
}

func TestToolNames(t *testing.T) {
	names := ToolNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 tool names, got %d", len(names))
	}
	if names[0] != "web_search" || names[1] != "web_fetch" {
		t.Fatalf("unexpected tool names: %v", names)
	}
}
