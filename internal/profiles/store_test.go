package profiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorePreservesProfileTemplate(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	created, err := store.Create(Profile{
		Name:           "Responses Provider",
		Template:       "responses",
		UpstreamFormat: "openai_responses",
		BaseURL:        "https://api.example.com/v1",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	profiles, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("List() returned %d profiles, want 1", len(profiles))
	}
	if profiles[0].ID != created.ID {
		t.Fatalf("List() profile ID = %q, want %q", profiles[0].ID, created.ID)
	}
	if profiles[0].Template != "responses" {
		t.Fatalf("List() template = %q, want responses", profiles[0].Template)
	}
}

func TestReasoningEffortMaxMetadataRoundTrip(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	created, err := store.Create(Profile{Name: "kimi", DefaultReasoningEffort: "low", Models: []ModelDef{{Name: "kimi", Model: "kimi", SupportsReasoningEffort: true, ReasoningEfforts: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, ReasoningEffortsSource: "declared"}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	if !stringSlicesEqual(got.Models[0].ReasoningEfforts, want) || got.Models[0].ReasoningEffortsSource != "declared" {
		t.Fatalf("model metadata = %#v", got.Models[0])
	}
}

func TestStoreRejectsUnsupportedDefaultReasoningEffort(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	_, err := store.Create(Profile{
		Name: "strict", DefaultModel: "m", DefaultReasoningEffort: "xhigh",
		Models: []ModelDef{{Name: "m", Model: "m", ReasoningEfforts: []string{"low", "medium", "high"}, ReasoningEffortsSource: "declared"}},
	})
	if err == nil || !strings.Contains(err.Error(), "不支持推理强度") {
		t.Fatalf("Create() error = %v", err)
	}
}

func TestNormalizeAddsReasoningEffortDefaults(t *testing.T) {
	profile := Normalize(Profile{
		DefaultModel: "grok-4.5",
		Models:       []ModelDef{{Name: "grok-4.5", Model: "grok-4.5"}},
	})
	if profile.DefaultReasoningEffort != "high" {
		t.Fatalf("DefaultReasoningEffort = %q", profile.DefaultReasoningEffort)
	}
	model := profile.Models[0]
	if !model.SupportsReasoningEffort {
		t.Fatal("SupportsReasoningEffort should default to true")
	}
	want := []string{"low", "medium", "high"}
	if !stringSlicesEqual(model.ReasoningEfforts, want) {
		t.Fatalf("ReasoningEfforts = %#v, want %#v", model.ReasoningEfforts, want)
	}
	if model.ReasoningEffortsSource != "default" {
		t.Fatalf("ReasoningEffortsSource = %q, want default", model.ReasoningEffortsSource)
	}
	declared := Normalize(Profile{Models: []ModelDef{{Name: "m", ReasoningEffortsSource: "declared"}}})
	if declared.Models[0].ReasoningEffortsSource != "declared" {
		t.Fatalf("explicit ReasoningEffortsSource was overwritten: %#v", declared.Models[0])
	}
}

func TestStoreRecoversCorruptProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	profiles, err := NewStore(path).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 0 {
		t.Fatalf("profiles = %#v, want empty recovery", profiles)
	}
	matches, err := filepath.Glob(path + ".corrupt-*.bak")
	if err != nil || len(matches) != 1 {
		t.Fatalf("corrupt backups = %#v, err = %v", matches, err)
	}
}
