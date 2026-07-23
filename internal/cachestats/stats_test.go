package cachestats

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectAggregatesHitRate(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessDir := filepath.Join(home, "sessions", "cwd", "sid-1")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatal(err)
	}
	summary := `{"info":{"id":"sid-1"},"current_model_id":"k3"}`
	if err := os.WriteFile(filepath.Join(sessDir, "summary.json"), []byte(summary), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	log := "" +
		`{"ts":"` + old + `","msg":"shell.turn.inference_done","sid":"sid-1","ctx":{"prompt_tokens":100,"cached_prompt_tokens":90,"completion_tokens":10}}` + "\n" +
		`{"ts":"` + now + `","msg":"shell.turn.inference_done","sid":"sid-1","ctx":{"prompt_tokens":100,"cached_prompt_tokens":80,"completion_tokens":5,"reasoning_tokens":2}}` + "\n" +
		`{"ts":"` + now + `","msg":"shell.turn.inference_done","sid":"sid-1","ctx":{"prompt_tokens":50,"cached_prompt_tokens":25,"completion_tokens":3}}` + "\n" +
		`{"ts":"` + now + `","msg":"other","sid":"sid-1","ctx":{"prompt_tokens":999,"cached_prompt_tokens":999}}` + "\n"
	if err := os.WriteFile(filepath.Join(logDir, "unified.jsonl"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := Collect(home, 24, "sid-1")
	if err != nil {
		t.Fatal(err)
	}
	if !report.LogExists {
		t.Fatal("log should exist")
	}
	if report.Overall.Turns != 2 {
		t.Fatalf("turns = %d, want 2 (48h event excluded)", report.Overall.Turns)
	}
	if report.Overall.PromptTokens != 150 || report.Overall.CachedPromptTokens != 105 {
		t.Fatalf("tokens = %d/%d", report.Overall.PromptTokens, report.Overall.CachedPromptTokens)
	}
	if report.Overall.HitRate == nil || *report.Overall.HitRate < 0.69 || *report.Overall.HitRate > 0.71 {
		t.Fatalf("hit_rate = %v", report.Overall.HitRate)
	}
	if len(report.ByModel) != 1 || report.ByModel[0].Model != "k3" {
		t.Fatalf("by_model = %#v", report.ByModel)
	}
	if report.Session == nil || report.Session.Turns != 2 {
		t.Fatalf("session = %#v", report.Session)
	}
}

func TestCollectMissingLog(t *testing.T) {
	report, err := Collect(t.TempDir(), 24, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.LogExists {
		t.Fatal("expected missing log")
	}
	if report.Overall.Turns != 0 {
		t.Fatalf("turns = %d", report.Overall.Turns)
	}
}
