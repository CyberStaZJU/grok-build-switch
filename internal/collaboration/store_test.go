package collaboration

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreReplaceRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "collaboration.json")
	store := NewStore(path)
	policy := NewPolicy("provider-1", testRoleAssignments())
	policy.ManagedArtifacts = []ManagedArtifact{{Path: "/tmp/role.toml", SHA256: strings.Repeat("a", 64)}}

	stored, err := store.Replace(policy)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt was not populated")
	}
	reloaded, err := NewStore(path).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ProviderID != policy.ProviderID || reloaded.Roles != policy.Roles || reloaded.ManagedArtifacts[0] != policy.ManagedArtifacts[0] {
		t.Fatalf("reloaded = %#v", reloaded)
	}
	assertPermissions(t, filepath.Dir(path), 0o700)
	assertPermissions(t, path, 0o600)
}

func TestStoreInvalidReplaceLeavesExistingBytesUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collaboration.json")
	store := NewStore(path)
	valid := NewPolicy("provider-1", testRoleAssignments())
	if _, err := store.Replace(valid); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	invalid := valid
	invalid.Roles.MainCoordinator.ReasoningEffort = "ultra"
	if _, err := store.Replace(invalid); err == nil {
		t.Fatal("expected validation error")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("invalid Replace changed bytes\nbefore=%s\nafter=%s", before, after)
	}
}

func TestStoreSnapshotMissing(t *testing.T) {
	_, err := NewStore(filepath.Join(t.TempDir(), "missing.json")).Snapshot()
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Snapshot() error = %v, want os.ErrNotExist", err)
	}
}

func TestStoreSnapshotMigratesV1InMemoryWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collaboration.json")
	updatedAt := time.Date(2026, time.August, 2, 3, 4, 5, 0, time.UTC)
	raw := []byte(`{
  "version": 1,
  "enabled": true,
  "provider_id": "provider-1",
  "models": {"coordinator":"provider-1:terra","evidence":"provider-1:luna","builder":"provider-1:sol"},
  "reasoning_effort": "max",
  "default_tier": "assurance",
  "budgets": {"economy":1,"focused":2,"assurance":3,"critical":4},
  "max_parallel": 1,
  "retry_limit": 1,
  "artifact_scope": "user",
  "managed_artifacts": [{"path":"/tmp/legacy-role.toml","sha256":"` + strings.Repeat("a", 64) + `"}],
  "updated_at": "` + updatedAt.Format(time.RFC3339) + `"
}
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	migrated, err := NewStore(path).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Version != CurrentVersion || !migrated.Enabled || migrated.ProviderID != "provider-1" || migrated.DefaultTier != "assurance" || migrated.Mode != ModeSingleProvider || migrated.FederationConsent != nil {
		t.Fatalf("migrated policy metadata = %#v", migrated)
	}
	wantRoles := NewPolicy("provider-1", RoleAssignments{
		MainCoordinator:               RoleAssignment{Model: "provider-1:terra", SpeedTier: SpeedTierStandard, ReasoningEffort: "max"},
		TaskDecomposition:             RoleAssignment{Model: "provider-1:luna", SpeedTier: SpeedTierStandard, ReasoningEffort: "max"},
		MainImplementation:            RoleAssignment{Model: "provider-1:terra", SpeedTier: SpeedTierStandard, ReasoningEffort: "max"},
		DifficultImplementationReview: RoleAssignment{Model: "provider-1:sol", SpeedTier: SpeedTierStandard, ReasoningEffort: "max"},
	}).Roles
	if migrated.Roles != wantRoles || !migrated.UpdatedAt.Equal(updatedAt) || len(migrated.ManagedArtifacts) != 1 {
		t.Fatalf("migrated policy = %#v", migrated)
	}
	afterSnapshot, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterSnapshot) != string(raw) {
		t.Fatalf("Snapshot rewrote v1 bytes\nwant=%s\ngot=%s", raw, afterSnapshot)
	}

	stored, err := NewStore(path).Replace(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != CurrentVersion || !stored.UpdatedAt.After(updatedAt) {
		t.Fatalf("stored policy = %#v", stored)
	}
	var encoded map[string]any
	v2Bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(v2Bytes, &encoded); err != nil {
		t.Fatal(err)
	}
	if got := int(encoded["version"].(float64)); got != CurrentVersion {
		t.Fatalf("persisted version = %d", got)
	}
	if _, ok := encoded["roles"]; !ok {
		t.Fatalf("persisted v4 policy has no roles: %s", v2Bytes)
	}
	if !strings.Contains(string(v2Bytes), `"speed_tier": "standard"`) {
		t.Fatalf("persisted v4 policy has no speed tiers: %s", v2Bytes)
	}
	if _, ok := encoded["models"]; ok {
		t.Fatalf("persisted v4 policy retained v1 models: %s", v2Bytes)
	}
	if _, ok := encoded["reasoning_effort"]; ok {
		t.Fatalf("persisted v4 policy retained v1 global effort: %s", v2Bytes)
	}
}

func TestStoreSnapshotMigratesV2InMemoryWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collaboration.json")
	updatedAt := time.Date(2026, time.August, 2, 5, 6, 7, 0, time.UTC)
	raw := []byte(`{
  "version": 2,
  "enabled": true,
  "provider_id": "provider-1",
  "roles": {
    "main_coordinator":{"model":"provider-1:terra","reasoning_effort":"high"},
    "task_decomposition":{"model":"provider-1:luna","reasoning_effort":"medium"},
    "main_implementation":{"model":"provider-1:terra","reasoning_effort":"xhigh"},
    "difficult_implementation_review":{"model":"provider-1:sol","reasoning_effort":"max"}
  },
  "default_tier": "critical",
  "budgets": {"economy":1,"focused":2,"assurance":3,"critical":4},
  "max_parallel": 1,
  "retry_limit": 1,
  "artifact_scope": "user",
  "managed_artifacts": [{"path":"/tmp/v2-workflow.rhai","sha256":"` + strings.Repeat("c", 64) + `"}],
  "updated_at": "` + updatedAt.Format(time.RFC3339) + `"
}
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	migrated, err := NewStore(path).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Version != CurrentVersion || !migrated.Enabled || migrated.ProviderID != "provider-1" || migrated.DefaultTier != "critical" || migrated.Mode != ModeSingleProvider || migrated.FederationConsent != nil {
		t.Fatalf("migrated v2 metadata = %#v", migrated)
	}
	for _, role := range migrated.Roles.entries() {
		if role.Assignment.SpeedTier != SpeedTierStandard {
			t.Fatalf("v2 role did not migrate to Standard: %#v", role)
		}
	}
	if !migrated.UpdatedAt.Equal(updatedAt) || len(migrated.ManagedArtifacts) != 1 {
		t.Fatalf("migrated v2 policy = %#v", migrated)
	}
	if after, readErr := os.ReadFile(path); readErr != nil || string(after) != string(raw) {
		t.Fatalf("Snapshot rewrote v2 bytes: err=%v\nwant=%s\ngot=%s", readErr, raw, after)
	}

	stored, err := NewStore(path).Replace(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != CurrentVersion || !stored.UpdatedAt.After(updatedAt) {
		t.Fatalf("stored policy = %#v", stored)
	}
	v3Bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(v3Bytes), `"version": 4`) || strings.Count(string(v3Bytes), `"speed_tier": "standard"`) != 4 {
		t.Fatalf("explicit Replace did not persist v4 speeds: %s", v3Bytes)
	}
}

func TestStoreSnapshotMigratesV3ToV4WithoutFederationConsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collaboration.json")
	raw := []byte(`{
  "version": 3,
  "enabled": true,
  "provider_id": "provider-1",
  "roles": {
    "main_coordinator":{"model":"provider-1:terra","speed_tier":"standard","reasoning_effort":"high"},
    "task_decomposition":{"model":"provider-1:luna","speed_tier":"standard","reasoning_effort":"medium"},
    "main_implementation":{"model":"provider-1:terra","speed_tier":"fast","reasoning_effort":"xhigh"},
    "difficult_implementation_review":{"model":"provider-1:sol","speed_tier":"fast","reasoning_effort":"max"}
  },
  "default_tier": "adaptive",
  "budgets": {"economy":1,"focused":2,"assurance":3,"critical":4},
  "max_parallel": 1,
  "retry_limit": 1,
  "artifact_scope": "user",
  "managed_artifacts": []
}
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, err := NewStore(path).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Version != CurrentVersion || migrated.Mode != ModeSingleProvider || migrated.FederationConsent != nil {
		t.Fatalf("migrated v3 policy = %#v", migrated)
	}
	if migrated.Roles.TaskDecomposition.DataScope != DataScopeRepositoryOnly || migrated.Roles.MainCoordinator.DataScope != DataScopePriorWork || migrated.Roles.MainImplementation.SpeedTier != SpeedTierFast {
		t.Fatalf("migrated v3 roles = %#v", migrated.Roles)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(raw) {
		t.Fatalf("Snapshot rewrote v3 bytes: err=%v", err)
	}
}

func TestStoreSnapshotMigratesDisabledV2RolesWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collaboration.json")
	raw := []byte(`{
  "version": 2,
  "enabled": false,
  "provider_id": "provider-1",
  "roles": {
    "main_coordinator":{"model":"provider-1:terra","reasoning_effort":"high"},
    "task_decomposition":{"model":"provider-1:luna","reasoning_effort":"medium"},
    "main_implementation":{"model":"provider-1:terra","reasoning_effort":"xhigh"},
    "difficult_implementation_review":{"model":"provider-1:sol","reasoning_effort":"max"}
  },
  "default_tier": "assurance",
  "budgets": {"economy":1,"focused":2,"assurance":3,"critical":4},
  "max_parallel": 1,
  "retry_limit": 1,
  "artifact_scope": "user",
  "managed_artifacts": []
}
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, err := NewStore(path).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Enabled || migrated.Roles.MainImplementation.Model != "provider-1:terra" || migrated.Roles.MainImplementation.SpeedTier != SpeedTierStandard {
		t.Fatalf("disabled v2 policy lost roles: %#v", migrated)
	}
	if after, readErr := os.ReadFile(path); readErr != nil || string(after) != string(raw) {
		t.Fatalf("Snapshot rewrote disabled v2 bytes: err=%v\nwant=%s\ngot=%s", readErr, raw, after)
	}
}

func TestStoreSnapshotMigratesDisabledV1MetadataAndManifestWithoutRoles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collaboration.json")
	updatedAt := time.Date(2026, time.August, 2, 6, 7, 8, 0, time.UTC)
	raw := []byte(`{
  "version": 1,
  "enabled": false,
  "provider_id": "provider-1",
  "models": {"coordinator":"provider-1:terra","evidence":"provider-1:luna","builder":"provider-1:sol"},
  "reasoning_effort": "max",
  "default_tier": "critical",
  "budgets": {"economy":1,"focused":2,"assurance":3,"critical":4},
  "max_parallel": 1,
  "retry_limit": 1,
  "artifact_scope": "user",
  "managed_artifacts": [{"path":"/tmp/legacy-workflow.rhai","sha256":"` + strings.Repeat("b", 64) + `"}],
  "updated_at": "` + updatedAt.Format(time.RFC3339) + `"
}
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	migrated, err := NewStore(path).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Version != CurrentVersion || migrated.Enabled || migrated.ProviderID != "provider-1" || migrated.DefaultTier != "critical" {
		t.Fatalf("migrated disabled policy metadata = %#v", migrated)
	}
	if migrated.Roles != (RoleAssignments{}) {
		t.Fatalf("disabled v1 policy unexpectedly retained active roles: %#v", migrated.Roles)
	}
	if !migrated.UpdatedAt.Equal(updatedAt) || len(migrated.ManagedArtifacts) != 1 || migrated.ManagedArtifacts[0].Path != "/tmp/legacy-workflow.rhai" {
		t.Fatalf("disabled v1 policy lost metadata or ownership: %#v", migrated)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(raw) {
		t.Fatalf("Snapshot rewrote disabled v1 bytes: err=%v\nwant=%s\ngot=%s", err, raw, after)
	}
}

func TestStoreRejectsMalformedV1MigrationWithoutChangingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collaboration.json")
	raw := []byte(`{
  "version": 1,
  "enabled": true,
  "provider_id": "provider-1",
  "models": {"coordinator":"provider-1:terra","evidence":"provider-1:luna","builder":"provider-1:sol"},
  "reasoning_effort": "none",
  "default_tier": "adaptive",
  "budgets": {"economy":1,"focused":2,"assurance":3,"critical":4},
  "max_parallel": 1,
  "retry_limit": 1,
  "artifact_scope": "user",
  "managed_artifacts": []
}
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewStore(path).Snapshot()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "reasoning effort") {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if after, readErr := os.ReadFile(path); readErr != nil || string(after) != string(raw) {
		t.Fatalf("invalid v1 migration changed bytes: err=%v\nwant=%s\ngot=%s", readErr, raw, after)
	}
}

func TestStoreRejectsTrailingDocumentsWithoutChangingBytes(t *testing.T) {
	base := `{
  "version": 1,
  "enabled": true,
  "provider_id": "provider-1",
  "models": {"coordinator":"provider-1:terra","evidence":"provider-1:luna","builder":"provider-1:sol"},
  "reasoning_effort": "max",
  "default_tier": "adaptive",
  "budgets": {"economy":1,"focused":2,"assurance":3,"critical":4},
  "max_parallel": 1,
  "retry_limit": 1,
  "artifact_scope": "user",
  "managed_artifacts": []
}`
	for _, suffix := range []string{"{}", " trailing"} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "collaboration.json")
			raw := []byte(base + suffix)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewStore(path).Snapshot()
			if err == nil {
				t.Fatal("expected strict trailing-data error")
			}
			if after, readErr := os.ReadFile(path); readErr != nil || string(after) != string(raw) {
				t.Fatalf("invalid trailing input changed bytes: err=%v\nwant=%s\ngot=%s", readErr, raw, after)
			}
		})
	}
}

func TestStoreRejectsUnknownFieldsWithoutChangingBytes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "v1",
			raw: `{
  "version": 1,
  "enabled": true,
  "provider_id": "provider-1",
  "models": {"coordinator":"provider-1:terra","evidence":"provider-1:luna","builder":"provider-1:sol"},
  "reasoning_effort": "max",
  "default_tier": "adaptive",
  "budgets": {"economy":1,"focused":2,"assurance":3,"critical":4},
  "max_parallel": 1,
  "retry_limit": 1,
  "artifact_scope": "user",
  "managed_artifacts": [],
  "unexpected": true
}
`,
		},
		{
			name: "v2 nested role",
			raw: `{
  "version": 2,
  "enabled": true,
  "provider_id": "provider-1",
  "roles": {
    "main_coordinator":{"model":"provider-1:terra","reasoning_effort":"high","unexpected":true},
    "task_decomposition":{"model":"provider-1:luna","reasoning_effort":"medium"},
    "main_implementation":{"model":"provider-1:terra","reasoning_effort":"xhigh"},
    "difficult_implementation_review":{"model":"provider-1:sol","reasoning_effort":"max"}
  },
  "default_tier": "adaptive",
  "budgets": {"economy":1,"focused":2,"assurance":3,"critical":4},
  "max_parallel": 1,
  "retry_limit": 1,
  "artifact_scope": "user",
  "managed_artifacts": []
}
`,
		},
		{
			name: "v3 nested role",
			raw: `{
  "version": 3,
  "enabled": true,
  "provider_id": "provider-1",
  "roles": {
    "main_coordinator":{"model":"provider-1:terra","speed_tier":"standard","reasoning_effort":"high","unexpected":true},
    "task_decomposition":{"model":"provider-1:luna","speed_tier":"standard","reasoning_effort":"medium"},
    "main_implementation":{"model":"provider-1:terra","speed_tier":"fast","reasoning_effort":"xhigh"},
    "difficult_implementation_review":{"model":"provider-1:sol","speed_tier":"fast","reasoning_effort":"max"}
  },
  "default_tier": "adaptive",
  "budgets": {"economy":1,"focused":2,"assurance":3,"critical":4},
  "max_parallel": 1,
  "retry_limit": 1,
  "artifact_scope": "user",
  "managed_artifacts": []
}
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "collaboration.json")
			raw := []byte(test.raw)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewStore(path).Snapshot()
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("Snapshot() error = %v", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != string(raw) {
				t.Fatalf("invalid snapshot changed bytes\nwant=%s\ngot=%s", raw, after)
			}
		})
	}
}

func TestPlanManagedArtifactsRejectsUnmanagedCollision(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "roles", "gbs-luna-evidence.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user-owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts := []RenderedArtifact{{Path: path, Content: []byte("managed\n"), SHA256: Hash([]byte("managed\n"))}}
	_, err := PlanManagedArtifacts(nil, artifacts)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "unmanaged") {
		t.Fatalf("PlanManagedArtifacts() error = %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "user-owned\n" {
		t.Fatalf("collision modified file: %q", got)
	}
}

func TestPlanManagedArtifactsAllowsMatchingManifestAndDetectsDrift(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "roles", "gbs-luna-evidence.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte("old-managed\n")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := []ManagedArtifact{{Path: path, SHA256: Hash(old)}}
	wanted := []byte("new-managed\n")
	plans, err := PlanManagedArtifacts(manifest, []RenderedArtifact{{Path: path, Content: wanted, SHA256: Hash(wanted)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].Action != ArtifactUpdate || !plans[0].Existed || plans[0].PreviousSHA256 != Hash(old) {
		t.Fatalf("plans = %#v", plans)
	}

	if err := os.WriteFile(path, []byte("user-edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanManagedArtifacts(manifest, []RenderedArtifact{{Path: path, Content: wanted, SHA256: Hash(wanted)}}); err == nil || !strings.Contains(strings.ToLower(err.Error()), "drift") {
		t.Fatalf("drift error = %v", err)
	}
}

func TestApplyManagedArtifactsRollsBackOnFailure(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "roles", "first.toml")
	secondPath := filepath.Join(root, "roles", "second.toml")
	if err := os.MkdirAll(filepath.Dir(firstPath), 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte("old-first\n")
	if err := os.WriteFile(firstPath, old, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := []ManagedArtifact{{Path: firstPath, SHA256: Hash(old)}}
	artifacts := []RenderedArtifact{
		{Path: firstPath, Content: []byte("new-first\n"), SHA256: Hash([]byte("new-first\n"))},
		{Path: secondPath, Content: []byte("new-second\n"), SHA256: Hash([]byte("new-second\n"))},
	}
	plans, err := PlanManagedArtifacts(manifest, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = ApplyManagedArtifacts(plans, func(path string, data []byte, mode os.FileMode) error {
		calls++
		if calls == 2 {
			return errors.New("injected write failure")
		}
		return atomicWriteFile(path, data, mode)
	})
	if err == nil || !strings.Contains(err.Error(), "injected write failure") {
		t.Fatalf("ApplyManagedArtifacts() error = %v", err)
	}
	got, readErr := os.ReadFile(firstPath)
	if readErr != nil || string(got) != string(old) {
		t.Fatalf("first file was not rolled back: %q, err=%v", got, readErr)
	}
	if _, statErr := os.Stat(secondPath); !os.IsNotExist(statErr) {
		t.Fatalf("second file exists after rollback: %v", statErr)
	}
}

func TestApplyManagedArtifactsRejectsStalePreviewWithoutOverwritingUserChanges(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "roles", "first.toml")
	secondPath := filepath.Join(root, "roles", "second.toml")
	if err := os.MkdirAll(filepath.Dir(firstPath), 0o700); err != nil {
		t.Fatal(err)
	}
	firstOld := []byte("first-old\n")
	secondOld := []byte("second-old\n")
	if err := os.WriteFile(firstPath, firstOld, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, secondOld, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := []ManagedArtifact{{Path: firstPath, SHA256: Hash(firstOld)}, {Path: secondPath, SHA256: Hash(secondOld)}}
	artifacts := []RenderedArtifact{
		{Path: firstPath, Content: []byte("first-new\n"), SHA256: Hash([]byte("first-new\n"))},
		{Path: secondPath, Content: []byte("second-new\n"), SHA256: Hash([]byte("second-new\n"))},
	}
	plans, err := PlanManagedArtifacts(manifest, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	concurrentEdit := []byte("user-edited-second\n")
	if err := os.WriteFile(secondPath, concurrentEdit, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedArtifacts(plans, nil); err == nil || !errors.Is(err, ErrArtifactDrift) {
		t.Fatalf("ApplyManagedArtifacts() error = %v, want ErrArtifactDrift", err)
	}
	if got, err := os.ReadFile(firstPath); err != nil || string(got) != string(firstOld) {
		t.Fatalf("earlier managed write was not rolled back: %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(secondPath); err != nil || string(got) != string(concurrentEdit) {
		t.Fatalf("concurrent user edit was overwritten: %q, err=%v", got, err)
	}
}

func TestApplyManagedArtifactsRejectsFileAppearingAfterPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles", "role.toml")
	content := []byte("managed\n")
	plans, err := PlanManagedArtifacts(nil, []RenderedArtifact{{Path: path, Content: content, SHA256: Hash(content)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	userContent := []byte("user-owned\n")
	if err := os.WriteFile(path, userContent, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedArtifacts(plans, nil); err == nil || !errors.Is(err, ErrArtifactConflict) {
		t.Fatalf("ApplyManagedArtifacts() error = %v, want ErrArtifactConflict", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(userContent) {
		t.Fatalf("appeared user file was overwritten: %q, err=%v", got, err)
	}
}

func TestRestoreManagedArtifactsPreservesConcurrentEditToCreatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles", "created.toml")
	desired := []byte("transaction desired\n")
	plans, err := PlanManagedArtifacts(nil, []RenderedArtifact{{Path: path, Content: desired, SHA256: Hash(desired)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedArtifacts(plans, nil); err != nil {
		t.Fatal(err)
	}
	concurrent := []byte("concurrent user edit\n")
	if err := os.WriteFile(path, concurrent, 0o600); err != nil {
		t.Fatal(err)
	}
	err = RestoreManagedArtifacts(plans)
	if err == nil || !strings.Contains(err.Error(), "rollback incomplete") {
		t.Fatalf("RestoreManagedArtifacts() error = %v", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != string(concurrent) {
		t.Fatalf("concurrent created-file edit was not preserved: %q, err=%v", got, readErr)
	}
}

func TestRestoreManagedArtifactsPreservesConcurrentEditToUpdatedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles", "updated.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := []byte("previous managed\n")
	if err := os.WriteFile(path, previous, 0o640); err != nil {
		t.Fatal(err)
	}
	desired := []byte("transaction desired\n")
	plans, err := PlanManagedArtifacts([]ManagedArtifact{{Path: path, SHA256: Hash(previous)}}, []RenderedArtifact{{Path: path, Content: desired, SHA256: Hash(desired)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedArtifacts(plans, nil); err != nil {
		t.Fatal(err)
	}
	concurrent := []byte("concurrent user edit\n")
	if err := os.WriteFile(path, concurrent, 0o600); err != nil {
		t.Fatal(err)
	}
	err = RestoreManagedArtifacts(plans)
	if err == nil || !strings.Contains(err.Error(), "rollback incomplete") {
		t.Fatalf("RestoreManagedArtifacts() error = %v", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != string(concurrent) {
		t.Fatalf("concurrent updated-file edit was not preserved: %q, err=%v", got, readErr)
	}
}

func TestApplyManagedArtifactsCreatesFilesWithPrivatePermissions(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "roles", "role.toml"),
		filepath.Join(root, "workflows", "workflow.rhai"),
	}
	artifacts := make([]RenderedArtifact, 0, len(paths))
	for _, path := range paths {
		content := []byte(filepath.Base(path) + "\n")
		artifacts = append(artifacts, RenderedArtifact{Path: path, Content: content, SHA256: Hash(content)})
	}
	plans, err := PlanManagedArtifacts(nil, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyManagedArtifacts(plans, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		assertPermissions(t, filepath.Dir(path), 0o700)
		assertPermissions(t, path, 0o600)
	}
}

func assertPermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %o, want %o", path, got, want)
	}
}
