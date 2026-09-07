package profiles

import "testing"

func TestKnownContextWindowMatchesLeavesAndFastAliases(t *testing.T) {
	cases := []struct {
		alias string
		want  int64
	}{
		{"k3-256k", 262144},
		{"K3-256K", 262144},
		{"subscription/codex/gpt-5.6-sol", 372000},
		{"subscription/codex/gpt-5.6-sol-fast", 372000},
		{"subscription/codex/gpt-5.6-terra", 372000},
		{"subscription/codex/gpt-5.6-luna", 372000},
		{"subscription/codex/gpt-6-astra", 272000},
		{"subscription/gemini/gemini-3.6-flash-high", 1048576},
		{"subscription/gemini/gemini-3.7-flash-high", 1048576},
		{"subscription/gemini/gemini-3.8-flash-high", 1048576},
		{"subscription/grok/grok-4.5", 500000},
		{"subscription/grok/grok-4.6", 500000},
		{"hy4-preview", 1000000},
		{"glm-5.3", 1000000},
		{"glm-5.3-flash", 1000000},
		{"kimi-k3-2", 1000000},
		{"hy3", 128000},
		{"deepseek-v4-flash", 1000000},
		{"deepseek-v4-pro", 1000000},
		{"gpt-5.6-sol", 372000},
		{"unknown-model", 0},
		{"subscription/codex/gpt-5.6-unknown", 0},
		{"subscription/codex/gpt-5.6-unknown-fast", 0},
		{"grok-4.5-mini", 0},
		{"", 0},
		{"  ", 0},
	}
	for _, tc := range cases {
		if got := KnownContextWindow(tc.alias); got != tc.want {
			t.Errorf("KnownContextWindow(%q) = %d, want %d", tc.alias, got, tc.want)
		}
	}
}

func TestApplyKnownContextDefaultsFillsOnlyUnsetModels(t *testing.T) {
	profile := Profile{Models: []ModelDef{
		{Name: "k3-256k"},
		{Name: "custom", Model: "subscription/grok/grok-4.6", ContextWindow: 0},
		{Name: "explicit", Model: "gpt-5.6-sol", ContextWindow: 12345},
		{Name: "mystery", Model: "mystery"},
	}}
	ApplyKnownContextDefaults(&profile)
	want := []int64{262144, 500000, 12345, 0}
	for i, model := range profile.Models {
		if model.ContextWindow != want[i] {
			t.Errorf("model %q ContextWindow = %d, want %d", model.Name, model.ContextWindow, want[i])
		}
	}
	// custom name wins over Model for lookup; Name takes precedence.
	if profile.Models[1].ContextWindow != 500000 {
		t.Errorf("leaf lookup should use Model when Name has no match, got %d", profile.Models[1].ContextWindow)
	}
}
