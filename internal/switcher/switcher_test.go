package switcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/profiles"
)

func TestActiveStatusReturnsMatchingProfile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[models]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	profile, err := store.Create(profiles.Profile{
		Name: "active", DefaultModel: "grok", Models: []profiles.ModelDef{{Name: "grok", Model: "grok"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Switcher{ConfigPath: configPath, Profiles: store}
	if _, err := s.Activate(profile.ID); err != nil {
		t.Fatal(err)
	}
	active, matches, err := s.ActiveStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !matches || active.ID != profile.ID {
		t.Fatalf("ActiveStatus() = (%#v, %v), want profile %q", active, matches, profile.ID)
	}
}

func TestMutationsDoNotCreateBackups(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[models]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	profile, err := store.Create(profiles.Profile{
		Name: "active", DefaultModel: "grok", Models: []profiles.ModelDef{{Name: "grok", Model: "grok"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Switcher{ConfigPath: configPath, Profiles: store}
	if _, err := s.Activate(profile.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteConfig([]byte("[telemetry]\nenabled = false\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "backups")); !os.IsNotExist(err) {
		t.Fatalf("mutation unexpectedly created backups directory: %v", err)
	}
}

func TestRestoreConfigStateRollsBackAtomically(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	s := &Switcher{ConfigPath: configPath}
	original := []byte("[telemetry]\nenabled = false\n")
	if err := s.RestoreConfigState(original, true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil || string(got) != string(original) {
		t.Fatalf("restored config = %q, err=%v", got, err)
	}
	if err := s.RestoreConfigState(nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("RestoreConfigState did not remove newly created config: %v", err)
	}
}

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
	s := &Switcher{ConfigPath: configPath, Profiles: store}
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
