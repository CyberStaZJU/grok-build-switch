package profiles

import "strings"

// knownContextWindows maps well-known model leaves to a reasonable default
// context window. Upstream /v1/models does not report context sizes, and Grok
// falls back to ~200k when config.toml omits context_window. Exact leaf
// matches only; no substring inference. Values are Switch-owned defaults and
// stay editable per model. Keep in sync with CONTEXT_WINDOW_SUGGESTIONS in
// ui/app.js.
var knownContextWindows = map[string]int64{
	// Codex 订阅代理（GPT-5.6 系列，272k）
	"gpt-5.6-terra": 272000,
	"gpt-5.6-sol":   272000,
	"gpt-5.6-luna":  272000,
	// Gemini 订阅代理（Gemini 系列 1M）
	"gemini-3.7-flash-high": 1048576,
	// Grok 订阅代理（对齐官方路由 grok-4.5 的 500k）
	"grok-4.5": 500000,
	"grok-4.6": 500000,
	// Kimi（名称声明 256k）
	"k3-256k": 262144,
	// CodeBuddy（与 internal/codebuddy/profile.go 一致）
	"hy3":               128000,
	"deepseek-v4-flash": 128000,
}

// KnownContextWindow returns the default context window for a well-known
// model alias (full alias or path leaf, tolerating one trailing "-fast"), or
// 0 when the model is unknown.
func KnownContextWindow(alias string) int64 {
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" {
		return 0
	}
	leaf := alias
	if i := strings.LastIndex(leaf, "/"); i >= 0 {
		leaf = leaf[i+1:]
	}
	if v, ok := knownContextWindows[leaf]; ok {
		return v
	}
	if strings.HasSuffix(leaf, "-fast") {
		if v, ok := knownContextWindows[strings.TrimSuffix(leaf, "-fast")]; ok {
			return v
		}
	}
	return 0
}

// ApplyKnownContextDefaults fills ContextWindow for models that leave it
// unset (0). Explicit values are never overwritten; unknown models keep 0 so
// Grok uses its own default. Saving a known model with 0 therefore re-applies
// the suggestion rather than persisting an unset override.
func ApplyKnownContextDefaults(profile *Profile) {
	for i := range profile.Models {
		if profile.Models[i].ContextWindow != 0 {
			continue
		}
		window := KnownContextWindow(profile.Models[i].Name)
		if window == 0 {
			window = KnownContextWindow(profile.Models[i].Model)
		}
		profile.Models[i].ContextWindow = window
	}
}
