package switcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/profiles"
)

func TestActivateRejectsMaxBeforeWritingConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	original := []byte("[models]\ndefault = \"old\"\ndefault_reasoning_effort = \"low\"\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	profile, err := store.Create(profiles.Profile{
		Name: "max-capable", DefaultModel: "kimi", DefaultReasoningEffort: "max",
		Models: []profiles.ModelDef{{Name: "kimi", Model: "kimi", SupportsReasoningEffort: true, ReasoningEfforts: []string{"low", "max"}, ReasoningEffortsSource: "declared"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Switcher{ConfigPath: configPath, BackupsDir: filepath.Join(dir, "backups"), Profiles: store}
	if _, err := s.Activate(profile.ID); err == nil || !strings.Contains(err.Error(), "不支持 max") {
		t.Fatalf("Activate() error = %v", err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("config was modified:\n%s", got)
	}
}
