package browseruse

import (
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

func TestToolNames(t *testing.T) {
	names := ToolNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 tool names, got %d", len(names))
	}
	if names[0] != "web_search" || names[1] != "web_fetch" {
		t.Fatalf("unexpected tool names: %v", names)
	}
}
