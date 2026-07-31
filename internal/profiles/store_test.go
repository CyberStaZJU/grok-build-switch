package profiles

import (
	"bytes"
	"errors"
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

func TestCreateAlwaysGeneratesFreshServerID(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	ids := []string{"generated-one", "generated-two"}
	store.idGenerator = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}

	first, err := store.Create(Profile{ID: "client-controlled", Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(Profile{ID: first.ID, Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "generated-one" || second.ID != "generated-two" {
		t.Fatalf("server IDs = %q, %q", first.ID, second.ID)
	}
	if first.ID == "client-controlled" || second.ID == first.ID {
		t.Fatalf("Create accepted or reused a client ID: %#v %#v", first, second)
	}
}

func TestCreateRetriesGeneratedIDCollisions(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	ids := []string{"existing", "existing", "unique"}
	store.idGenerator = func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	first, err := store.Create(Profile{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(Profile{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "existing" || second.ID != "unique" {
		t.Fatalf("IDs = %q, %q", first.ID, second.ID)
	}
}

func TestCreateExhaustedIDCollisionsLeavesBytesUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	store := NewStore(path)
	store.idGenerator = func() (string, error) { return "existing", nil }
	if _, err := store.Create(Profile{Name: "existing"}); err != nil {
		t.Fatal(err)
	}
	before := readBytes(t, path)

	if _, err := store.Create(Profile{Name: "rejected"}); err == nil || !strings.Contains(err.Error(), "exhausted attempts") {
		t.Fatalf("Create() error = %v", err)
	}
	if after := readBytes(t, path); !bytes.Equal(after, before) {
		t.Fatalf("collision-exhausted Create changed profiles.json\nbefore=%s\nafter=%s", before, after)
	}
}

func TestCreateIDGenerationFailureLeavesBytesUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	store := NewStore(path)
	if _, err := store.Create(Profile{Name: "existing"}); err != nil {
		t.Fatal(err)
	}
	before := readBytes(t, path)
	store.idGenerator = func() (string, error) { return "", errors.New("entropy unavailable") }

	if _, err := store.Create(Profile{ID: "client-id", Name: "rejected"}); err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("Create() error = %v", err)
	}
	if after := readBytes(t, path); !bytes.Equal(after, before) {
		t.Fatalf("failed Create changed profiles.json\nbefore=%s\nafter=%s", before, after)
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

func TestNormalizePreservesReasoningCapabilityMetadata(t *testing.T) {
	profile := Normalize(Profile{
		DefaultModel: "plain-model",
		Models:       []ModelDef{{Name: "plain-model", Model: "plain-model"}},
	})
	if profile.DefaultReasoningEffort != "" {
		t.Fatalf("DefaultReasoningEffort = %q", profile.DefaultReasoningEffort)
	}
	model := profile.Models[0]
	if model.SupportsReasoningEffort || len(model.ReasoningEfforts) != 0 || model.ReasoningEffortsSource != "" {
		t.Fatalf("Normalize fabricated reasoning metadata: %#v", model)
	}
	declared := Normalize(Profile{Models: []ModelDef{{Name: "m", ReasoningEfforts: []string{"low", "high", "low"}, ReasoningEffortsSource: "declared"}}})
	if !declared.Models[0].SupportsReasoningEffort || !stringSlicesEqual(declared.Models[0].ReasoningEfforts, []string{"low", "high"}) || declared.Models[0].ReasoningEffortsSource != "declared" {
		t.Fatalf("explicit reasoning metadata was not preserved: %#v", declared.Models[0])
	}
}

func TestUpdateRetainsPathID(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	created, err := store.Create(Profile{Name: "before"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(created.ID, Profile{ID: "client-replacement", Name: "after"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID {
		t.Fatalf("Update() ID = %q, want path ID %q", updated.ID, created.ID)
	}
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.Name != "after" {
		t.Fatalf("stored profile = %#v", got)
	}
	if _, err := store.Get("client-replacement"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Get(client replacement) error = %v, want not exist", err)
	}
}

func TestUpdateMissingProfileLeavesBytesUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	store := NewStore(path)
	if _, err := store.Create(Profile{Name: "existing"}); err != nil {
		t.Fatal(err)
	}
	before := readBytes(t, path)

	if _, err := store.Update("missing", Profile{ID: "client-replacement", Name: "ignored"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Update() error = %v, want not exist", err)
	}
	if after := readBytes(t, path); !bytes.Equal(after, before) {
		t.Fatalf("missing Update changed profiles.json\nbefore=%s\nafter=%s", before, after)
	}
}

func TestUpdateValidationFailureLeavesBytesUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	store := NewStore(path)
	created, err := store.Create(Profile{
		Name: "before", DefaultModel: "m", DefaultReasoningEffort: "low",
		Models: []ModelDef{{Name: "m", Model: "m", ReasoningEfforts: []string{"low"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	before := readBytes(t, path)
	_, err = store.Update(created.ID, Profile{
		ID: "client-replacement", Name: "invalid", DefaultModel: "m", DefaultReasoningEffort: "high",
		Models: []ModelDef{{Name: "m", Model: "m", ReasoningEfforts: []string{"low"}}},
	})
	if err == nil {
		t.Fatal("Update() accepted invalid profile")
	}
	if after := readBytes(t, path); !bytes.Equal(after, before) {
		t.Fatalf("failed Update changed profiles.json\nbefore=%s\nafter=%s", before, after)
	}
}

func TestStoreQuarantinesDuplicateIDsWithoutRoutingThem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	original := []byte(`[
  {"id":"duplicate","name":"first","default_model":"m1","models":[{"name":"m1","model":"m1"}]},
  {"id":"duplicate","name":"second","default_model":"m2","models":[{"name":"m2","model":"m2"}]}
]
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	profiles, err := NewStore(path).List()
	assertIdentityQuarantine(t, path, original, profiles, err, `duplicate profile id "duplicate"`)
}

func TestReadLegacyRoutingFieldsQuarantinesInvalidIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	original := []byte(`[
  {"id":"duplicate","web_search_model":"search"},
  {"id":"duplicate","subagents_default_model":"sub"}
]
`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	fields, err := NewStore(path).ReadLegacyRoutingFields()
	if fields != nil {
		t.Fatalf("invalid profiles exposed legacy routing fields: %#v", fields)
	}
	assertIdentityQuarantine(t, path, original, nil, err, `duplicate profile id "duplicate"`)
}

func TestStoreQuarantinesEmptyID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	original := []byte("[{\"id\":\"\",\"name\":\"missing\"}]\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	profiles, err := NewStore(path).List()
	assertIdentityQuarantine(t, path, original, profiles, err, "profile at index 0 has empty id")
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

func assertIdentityQuarantine(t *testing.T, path string, original []byte, profiles []Profile, err error, wantError string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), wantError) || !strings.Contains(err.Error(), "profiles quarantined at") {
		t.Fatalf("List() error = %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("quarantined profiles participated in List: %#v", profiles)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("original profiles path still exists or stat failed: %v", statErr)
	}
	matches, globErr := filepath.Glob(path + ".corrupt-*.bak")
	if globErr != nil || len(matches) != 1 {
		t.Fatalf("quarantine backups = %#v, err = %v", matches, globErr)
	}
	if got := readBytes(t, matches[0]); !bytes.Equal(got, original) {
		t.Fatalf("quarantine bytes changed\nwant=%q\ngot=%q", original, got)
	}
	info, statErr := os.Stat(matches[0])
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("quarantine mode = %o, want 600", info.Mode().Perm())
	}
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
