package switcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/profiles"
)

func TestActivateAllowsMaxWhenDefaultModelAdvertisesIt(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[models]\n"), 0o600); err != nil {
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
	if _, err := s.Activate(profile.ID); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "default_reasoning_effort = 'max'") && !strings.Contains(string(got), "default_reasoning_effort = \"max\"") {
		t.Fatalf("max effort was not written:\n%s", got)
	}
}
