package codebuddy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoadCatalogReadsLatestWorkBuddyCLIModels(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".workbuddy", "local_storage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(dir, "entry_old.info")
	if err := os.WriteFile(oldPath, []byte(`[{
		"ts": 1000,
		"data": {
			"agents": [{"name":"cli","models":["hy3"]}],
			"models": [{"id":"hy3","name":"Hy3","supportsToolCall":true}]
		}
	}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, "wb_entry_new.info")
	if err := os.WriteFile(newPath, []byte(`[{
		"ts": 2000,
		"data": {
			"agents": [{"name":"other","models":["ignored"]},{"name":"cli","models":["kimi-k3-2","hy4-preview","no-tools","missing","kimi-k3-2"]}],
			"models": [
				{"id":"kimi-k3-2","name":"Kimi-K3","maxInputTokens":1000000,"maxOutputTokens":32000,"supportsReasoning":true,"supportsToolCall":true,"reasoning":{"canDisableThinking":true,"defaultEffort":"high","supportedEfforts":["low","high","xhigh"]}},
				{"id":"hy4-preview","name":"Hy4 preview","maxAllowedSize":1000000,"maxOutputTokens":64000,"supportsReasoning":true,"supportsToolCall":true,"reasoning":{"defaultEffort":"high","supportedEfforts":["high"]}},
				{"id":"no-tools","supportsToolCall":false}
			]
		}
	}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(oldPath, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, now, now); err != nil {
		t.Fatal(err)
	}

	catalog := LoadCatalog(home)
	if catalog.Source != "workbuddy_local_catalog" {
		t.Fatalf("source = %q", catalog.Source)
	}
	if got := catalog.IDs(); !reflect.DeepEqual(got, []string{"kimi-k3-2", "hy4-preview"}) {
		t.Fatalf("ids = %#v", got)
	}
	kimi, ok := catalog.Find("kimi-k3-2")
	if !ok || kimi.MaxInputTokens != 1000000 || kimi.MaxOutputTokens != 32000 || !reflect.DeepEqual(kimi.ReasoningEfforts, []string{"low", "high", "xhigh"}) {
		t.Fatalf("kimi = %#v", kimi)
	}
	hy4, ok := catalog.Find("hy4-preview")
	if !ok || hy4.MaxInputTokens != 1000000 || hy4.MaxOutputTokens != 64000 {
		t.Fatalf("hy4 = %#v", hy4)
	}
}

func TestLoadCatalogUsesNewestEmbeddedTimestampAcrossFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".workbuddy", "local_storage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	newerCatalog := filepath.Join(dir, "entry_newer.info")
	if err := os.WriteFile(newerCatalog, []byte(`[{"ts":5000,"data":{"agents":[{"name":"cli","models":["glm-5.3"]}],"models":[{"id":"glm-5.3","supportsToolCall":true}]}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	staleTouchedLater := filepath.Join(dir, "entry_stale.info")
	if err := os.WriteFile(staleTouchedLater, []byte(`[{"ts":1000,"data":{"agents":[{"name":"cli","models":["hy3"]}],"models":[{"id":"hy3","supportsToolCall":true}]}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(newerCatalog, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleTouchedLater, now, now); err != nil {
		t.Fatal(err)
	}

	catalog := LoadCatalog(home)
	if got := catalog.IDs(); !reflect.DeepEqual(got, []string{"glm-5.3"}) {
		t.Fatalf("ids = %#v, want newest embedded timestamp", got)
	}
	if !catalog.UpdatedAt.Equal(time.UnixMilli(5000)) {
		t.Fatalf("updated_at = %v", catalog.UpdatedAt)
	}
}

func TestLoadCatalogFallsBackForInvalidOrMissingCache(t *testing.T) {
	catalog := LoadCatalog(t.TempDir())
	if catalog.Source != "switch_builtin" {
		t.Fatalf("source = %q", catalog.Source)
	}
	for _, id := range []string{"kimi-k3-2", "hy4-preview", "glm-5.3", "glm-5.3-flash"} {
		if _, ok := catalog.Find(id); !ok {
			t.Fatalf("fallback missing %q", id)
		}
	}
}

func TestIsValidModelID(t *testing.T) {
	for _, id := range []string{"kimi-k3-2", "glm-5.3-flash", "vendor/model:v1"} {
		if !IsValidModelID(id) {
			t.Fatalf("valid id rejected: %q", id)
		}
	}
	for _, id := range []string{"", "model@provider", "two words", "../bad?x=1", "模型"} {
		if IsValidModelID(id) {
			t.Fatalf("invalid id accepted: %q", id)
		}
	}
}

func TestModelDefinitionUsesCatalogLimitsAndEfforts(t *testing.T) {
	def := ModelDefinition(CatalogModel{
		ID:                "glm-5.3",
		MaxInputTokens:    1000000,
		MaxOutputTokens:   48000,
		SupportsReasoning: true,
		SupportsToolCall:  true,
		ReasoningEfforts:  []string{"low", "high", "xhigh"},
	}, "http://127.0.0.1:1/v1", "ck_test")
	if def.ContextWindow != 1000000 || def.MaxCompletionTokens != 48000 || !def.SupportsReasoningEffort || def.ReasoningEffortsSource != "declared" {
		t.Fatalf("definition = %#v", def)
	}
	if def.StreamToolCalls == nil || *def.StreamToolCalls {
		t.Fatalf("stream_tool_calls = %#v", def.StreamToolCalls)
	}
}
