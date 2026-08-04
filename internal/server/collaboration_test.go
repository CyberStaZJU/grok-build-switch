package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"grok_switch/internal/collaboration"
	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
	"grok_switch/internal/switcher"
)

func TestCollaborationApplyPreservesOrdinaryRoutingSlots(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	providerID := stored.ActiveProviderID
	policy := stored.ProviderPolicies[providerID]
	policy.DefaultReasoningEffort = "max"
	policy.WebSearch = providerID + ":subscription/codex/gpt-5.6-sol"
	policy.Subagents.Explore = providerID + ":subscription/codex/gpt-5.6-luna"
	policy.Subagents.Plan = providerID + ":subscription/codex/gpt-5.6-sol"
	stored.ProviderPolicies[providerID] = policy
	items, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	profile := items[0]
	for i := range profile.Models {
		if profile.Models[i].Name == "subscription/codex/gpt-5.6-sol" {
			profile.Models[i].APIBackend = "responses"
			profile.Models[i].SupportsBackendSearch = true
		}
	}
	if _, err := s.Profiles.Update(profile.ID, profile); err != nil {
		t.Fatal(err)
	}
	items, err = s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	hydrated, err := routing.ProjectWithSnapshot(items, stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Switcher.ApplyRouting(hydrated); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Routing.Replace(hydrated); err != nil {
		t.Fatal(err)
	}

	before, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	beforePolicy := before.ProviderPolicies[providerID]
	preview := previewCollaboration(t, s, request)
	if preview.RoutingAfter.WebSearch != beforePolicy.WebSearch || preview.RoutingAfter.Subagents != beforePolicy.Subagents {
		t.Fatalf("preview overwrote ordinary routing slots: before=%#v after=%#v", beforePolicy, preview.RoutingAfter)
	}
	applyBody := strings.TrimSuffix(request, "}") + `,"confirmed":true,"fingerprint":"` + preview.Fingerprint + `"}`
	response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", applyBody)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	after, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	afterPolicy := after.ProviderPolicies[providerID]
	if afterPolicy.WebSearch != beforePolicy.WebSearch || afterPolicy.Subagents != beforePolicy.Subagents {
		t.Fatalf("apply overwrote ordinary routing slots: before=%#v after=%#v", beforePolicy, afterPolicy)
	}
	if afterPolicy.Default == beforePolicy.Default || afterPolicy.DefaultReasoningEffort == beforePolicy.DefaultReasoningEffort {
		t.Fatalf("apply did not align default to the main coordinator: before=%#v after=%#v", beforePolicy, afterPolicy)
	}
}

func TestCollaborationCoordinatorSpeedResolvesConcreteDefaultAndChangesFingerprint(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	fastPreview := previewCollaboration(t, s, request)
	if !strings.HasSuffix(fastPreview.RoutingAfter.Default, ":subscription/codex/gpt-5.6-terra-fast") {
		t.Fatalf("Fast coordinator default = %q", fastPreview.RoutingAfter.Default)
	}
	var fastCoordinatorRole string
	for _, artifact := range fastPreview.Artifacts {
		if filepath.Base(artifact.Path) == collaboration.MainCoordinatorRoleName+".toml" {
			fastCoordinatorRole = artifact.Content
			break
		}
	}
	if !strings.Contains(fastCoordinatorRole, `model = "subscription/codex/gpt-5.6-terra-fast"`) {
		t.Fatalf("Fast coordinator artifact did not use concrete alias:\n%s", fastCoordinatorRole)
	}

	standardRequest := decodeCollaborationRequestMap(t, request)
	roles := standardRequest["roles"].(map[string]any)
	for _, rawRole := range roles {
		rawRole.(map[string]any)["speed_tier"] = collaboration.SpeedTierStandard
	}
	standardPreview := previewCollaboration(t, s, encodeCollaborationRequestMap(t, standardRequest))
	if !strings.HasSuffix(standardPreview.RoutingAfter.Default, ":subscription/codex/gpt-5.6-terra") || strings.HasSuffix(standardPreview.RoutingAfter.Default, "-fast") {
		t.Fatalf("Standard coordinator default = %q", standardPreview.RoutingAfter.Default)
	}
	if standardPreview.Fingerprint == fastPreview.Fingerprint {
		t.Fatal("changing only speed tiers did not change preview fingerprint")
	}
	if strings.Contains(strings.Join(standardPreview.Warnings, "\n"), "更多订阅 credits") {
		t.Fatalf("all-Standard preview retained Fast credit warning: %#v", standardPreview.Warnings)
	}
}

func TestCollaborationPreviewRejectsMissingSpeedTierAndConcreteFastEffortDrift(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	beforeConfig := readRequiredFile(t, s.Switcher.ConfigPath)
	beforeRouting := readRequiredFile(t, s.Routing.Path())

	missingSpeed := decodeCollaborationRequestMap(t, request)
	delete(missingSpeed["roles"].(map[string]any)["main_coordinator"].(map[string]any), "speed_tier")
	response := invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", encodeCollaborationRequestMap(t, missingSpeed))
	if response.Code != http.StatusBadRequest || !strings.Contains(strings.ToLower(response.Body.String()), "speed tier") {
		t.Fatalf("missing speed status=%d body=%s", response.Code, response.Body.String())
	}

	items, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	profile := items[0]
	for i := range profile.Models {
		if profile.Models[i].Name == "subscription/codex/gpt-5.6-terra-fast" {
			profile.Models[i].ReasoningEfforts = []string{"xhigh", "max"}
		}
	}
	if _, err := s.Profiles.Update(profile.ID, profile); err != nil {
		t.Fatal(err)
	}
	response = invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", request)
	if response.Code != http.StatusBadRequest || !strings.Contains(strings.ToLower(response.Body.String()), "reasoning effort") {
		t.Fatalf("Fast concrete effort drift status=%d body=%s", response.Code, response.Body.String())
	}
	assertFileBytesEqual(t, s.Switcher.ConfigPath, beforeConfig)
	assertFileBytesEqual(t, s.Routing.Path(), beforeRouting)
	if _, err := os.Stat(s.Collaboration.Path()); !os.IsNotExist(err) {
		t.Fatalf("rejected previews created policy: %v", err)
	}
}

func TestCollaborationStatusStrictlyDetectsCoordinatorDefaultDrift(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace func(string) string
	}{
		{
			name: "default route",
			replace: func(text string) string {
				return strings.Replace(text, "default = 'subscription/codex/gpt-5.6-terra-fast'", "default = 'subscription/codex/gpt-5.6-sol'", 1)
			},
		},
		{
			name: "default reasoning effort",
			replace: func(text string) string {
				return strings.Replace(text, "default_reasoning_effort = 'high'", "default_reasoning_effort = 'medium'", 1)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, request := newCollaborationTestServer(t)
			applyCollaborationRequest(t, s, request)
			before := string(readRequiredFile(t, s.Switcher.ConfigPath))
			after := tc.replace(before)
			if after == before {
				t.Fatalf("test did not mutate config:\n%s", before)
			}
			if err := os.WriteFile(s.Switcher.ConfigPath, []byte(after), 0o600); err != nil {
				t.Fatal(err)
			}
			status := collaborationStatus(t, s)
			if status.Valid || !strings.Contains(strings.Join(status.Issues, "\n"), "config.toml 与 collaboration 路由不一致") {
				t.Fatalf("drift status = %#v", status)
			}
		})
	}
}

func TestCollaborationStatusReportsMissingSavedFastWithoutFallback(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	preview := previewCollaboration(t, s, request)
	applyBody := strings.TrimSuffix(request, "}") + `,"confirmed":true,"fingerprint":"` + preview.Fingerprint + `"}`
	if response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", applyBody); response.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.String())
	}

	items, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	profile := items[0]
	kept := profile.Models[:0]
	for _, model := range profile.Models {
		if model.SpeedTier != profiles.SpeedTierFast {
			kept = append(kept, model)
		}
	}
	profile.Models = kept
	if _, err := s.Profiles.Update(profile.ID, profile); err != nil {
		t.Fatal(err)
	}

	response := invokeCollaboration(t, s.handleCollaboration, http.MethodGet, "/api/collaboration", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var status collaborationStatusDTO
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.Valid || status.Policy == nil || status.Policy.Roles.MainCoordinator.SpeedTier != collaboration.SpeedTierFast {
		t.Fatalf("missing Fast status = %#v", status)
	}
	if !strings.Contains(strings.ToLower(strings.Join(status.Issues, "\n")), "refusing to fall back") {
		t.Fatalf("missing Fast status did not report fail-closed resolution: %#v", status.Issues)
	}
}

func TestCollaborationPreviewIsSideEffectFreeAndDeterministic(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	beforeConfig := readRequiredFile(t, s.Switcher.ConfigPath)
	beforeRouting := readRequiredFile(t, s.Routing.Path())

	first := invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", request)
	if first.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", first.Code, first.Body.String())
	}
	second := invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", request)
	if second.Code != http.StatusOK || second.Body.String() != first.Body.String() {
		t.Fatalf("preview not deterministic\nfirst=%s\nsecond=%s", first.Body.String(), second.Body.String())
	}
	var preview collaborationPreviewDTO
	if err := json.Unmarshal(first.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Fingerprint == "" || len(preview.Artifacts) != 9 || !preview.RoutingChanged || !preview.ConfigChanged {
		t.Fatalf("preview = %#v", preview)
	}
	if !strings.Contains(preview.ConfigAfter, `default_reasoning_effort = 'high'`) || !strings.Contains(preview.ConfigAfter, `default = 'subscription/codex/gpt-5.6-terra-fast'`) {
		t.Fatalf("config preview missing resolved Fast main coordinator routing:\n%s", preview.ConfigAfter)
	}
	if !strings.Contains(strings.Join(preview.Warnings, "\n"), "更多订阅 credits") {
		t.Fatalf("Fast preview omitted higher-credit warning: %#v", preview.Warnings)
	}
	if _, err := os.Stat(s.Collaboration.Path()); !os.IsNotExist(err) {
		t.Fatalf("preview created collaboration policy: %v", err)
	}
	for _, artifact := range preview.Artifacts {
		if _, err := os.Stat(artifact.Path); !os.IsNotExist(err) {
			t.Fatalf("preview created artifact %s: %v", artifact.Path, err)
		}
	}
	assertFileBytesEqual(t, s.Switcher.ConfigPath, beforeConfig)
	assertFileBytesEqual(t, s.Routing.Path(), beforeRouting)
}

func TestCollaborationApplyRequiresExplicitConfirmationAndMatchingFingerprint(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	preview := previewCollaboration(t, s, request)
	beforeConfig := readRequiredFile(t, s.Switcher.ConfigPath)
	beforeRouting := readRequiredFile(t, s.Routing.Path())

	for _, body := range []string{
		request,
		strings.TrimSuffix(request, "}") + `,"confirmed":true,"fingerprint":"stale"}`,
	} {
		response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		assertFileBytesEqual(t, s.Switcher.ConfigPath, beforeConfig)
		assertFileBytesEqual(t, s.Routing.Path(), beforeRouting)
		if _, err := os.Stat(s.Collaboration.Path()); !os.IsNotExist(err) {
			t.Fatalf("invalid apply created policy: %v", err)
		}
	}

	applyBody := strings.TrimSuffix(request, "}") + `,"confirmed":true,"fingerprint":"` + preview.Fingerprint + `"}`
	response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", applyBody)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var applied struct {
		Status collaborationStatusDTO `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	policy, err := s.Collaboration.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Enabled || len(policy.ManagedArtifacts) != 9 {
		t.Fatalf("policy = %#v", policy)
	}
	if !applied.Status.Configured || !applied.Status.Valid || applied.Status.Policy == nil || applied.Status.Policy.UpdatedAt.IsZero() || !applied.Status.Policy.UpdatedAt.Equal(policy.UpdatedAt) {
		t.Fatalf("apply response did not return the persisted policy: response=%#v stored=%#v", applied.Status, policy)
	}
	for _, artifact := range policy.ManagedArtifacts {
		content := readRequiredFile(t, artifact.Path)
		if collaboration.Hash(content) != artifact.SHA256 {
			t.Fatalf("artifact hash mismatch: %s", artifact.Path)
		}
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	active := stored.ProviderPolicies[stored.ActiveProviderID]
	wantCoordinatorFast := policy.Roles.MainCoordinator.Model + "-fast"
	if stored.ActiveProviderID != policy.ProviderID || active.Default != wantCoordinatorFast || active.DefaultReasoningEffort != policy.Roles.MainCoordinator.ReasoningEffort {
		t.Fatalf("routing after apply = %#v policy=%#v, want resolved coordinator %q", stored, policy, wantCoordinatorFast)
	}
	if active.Subagents.Explore != "" || active.Subagents.Plan != "" {
		t.Fatalf("collaboration unexpectedly took ownership of generic subagent routing: %#v", active.Subagents)
	}
}

func TestCollaborationPreviewRejectsUnknownJSONAndUnmanagedCollision(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	var legacy map[string]any
	if err := json.Unmarshal([]byte(request), &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "roles")
	legacy["models"] = map[string]string{
		"coordinator": "legacy:terra",
		"evidence":    "legacy:luna",
		"builder":     "legacy:sol",
	}
	legacy["reasoning_effort"] = "max"
	legacyRequest, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		strings.TrimSuffix(request, "}") + `,"unknown":true}`,
		request + `{}`,
		string(legacyRequest),
	} {
		response := invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}

	paths := collaboration.ArtifactPathsForGrokHome(s.Paths.GrokHome)
	if err := os.MkdirAll(filepath.Dir(paths.TaskDecompositionRole), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.TaskDecompositionRole, []byte("user-owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", request)
	if response.Code != http.StatusConflict || !strings.Contains(strings.ToLower(response.Body.String()), "unmanaged") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	got := readRequiredFile(t, paths.TaskDecompositionRole)
	if string(got) != "user-owned\n" {
		t.Fatalf("collision changed file: %q", got)
	}
}

func TestCollaborationDisableStopsUseWithoutDeletingManagedArtifacts(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	preview := previewCollaboration(t, s, request)
	applyBody := strings.TrimSuffix(request, "}") + `,"confirmed":true,"fingerprint":"` + preview.Fingerprint + `"}`
	if response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", applyBody); response.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.String())
	}
	before, err := s.Collaboration.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	beforeRouting := readRequiredFile(t, s.Routing.Path())
	// Deliberately drift config.toml. Disabling must remain policy-only and must
	// not use this request as an opportunity to reapply routing.
	driftedConfig := []byte("# user-edited drift\n[features]\ntelemetry = false\n")
	if err := os.WriteFile(s.Switcher.ConfigPath, driftedConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	disableRequest := `{"version":4,"enabled":false}`
	disablePreview := previewCollaboration(t, s, disableRequest)
	if disablePreview.RoutingChanged || disablePreview.ConfigChanged || len(disablePreview.Artifacts) != 0 {
		t.Fatalf("disable preview = %#v", disablePreview)
	}
	if disablePreview.ConfigBefore != string(driftedConfig) || disablePreview.ConfigAfter != string(driftedConfig) {
		t.Fatalf("disable preview changed drifted config\nbefore=%q\nafter=%q", disablePreview.ConfigBefore, disablePreview.ConfigAfter)
	}
	disableBody := `{"version":4,"enabled":false,"confirmed":true,"fingerprint":"` + disablePreview.Fingerprint + `"}`
	response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", disableBody)
	if response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}
	var disabled struct {
		Status collaborationStatusDTO `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &disabled); err != nil {
		t.Fatal(err)
	}
	after, err := s.Collaboration.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled || after.ProviderID != before.ProviderID || after.Roles != before.Roles || after.DefaultTier != before.DefaultTier || len(after.ManagedArtifacts) != len(before.ManagedArtifacts) {
		t.Fatalf("disabled policy did not preserve collaboration configuration and ownership: before=%#v after=%#v", before, after)
	}
	if !disabled.Status.Configured || !disabled.Status.Valid || disabled.Status.Drifted || disabled.Status.Policy == nil || disabled.Status.Policy.Enabled || disabled.Status.Policy.UpdatedAt.IsZero() || !disabled.Status.Policy.UpdatedAt.Equal(after.UpdatedAt) {
		t.Fatalf("disable response did not return the persisted disabled policy: response=%#v stored=%#v", disabled.Status, after)
	}
	assertFileBytesEqual(t, s.Switcher.ConfigPath, driftedConfig)
	assertFileBytesEqual(t, s.Routing.Path(), beforeRouting)
	for _, artifact := range before.ManagedArtifacts {
		if _, err := os.Stat(artifact.Path); err != nil {
			t.Fatalf("disable removed managed artifact %s: %v", artifact.Path, err)
		}
	}
}

func TestCollaborationDisablePreviewWorksWhenRoutingStateIsUnavailable(t *testing.T) {
	s, _ := newCollaborationTestServer(t)
	before := collaboration.NewPolicy("missing-provider", collaboration.RoleAssignments{
		MainCoordinator:               collaboration.RoleAssignment{Model: "missing:terra", SpeedTier: collaboration.SpeedTierFast, ReasoningEffort: "max"},
		TaskDecomposition:             collaboration.RoleAssignment{Model: "missing:luna", SpeedTier: collaboration.SpeedTierStandard, ReasoningEffort: "max"},
		MainImplementation:            collaboration.RoleAssignment{Model: "missing:terra", SpeedTier: collaboration.SpeedTierFast, ReasoningEffort: "max"},
		DifficultImplementationReview: collaboration.RoleAssignment{Model: "missing:sol", SpeedTier: collaboration.SpeedTierFast, ReasoningEffort: "max"},
	})
	artifactPaths := collaboration.ArtifactPathsForGrokHome(s.Paths.GrokHome)
	for _, path := range artifactPaths.CanonicalPaths() {
		before.ManagedArtifacts = append(before.ManagedArtifacts, collaboration.ManagedArtifact{Path: path, SHA256: strings.Repeat("a", 64)})
	}
	if _, err := s.Collaboration.Replace(before); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.Routing.Path()); err != nil {
		t.Fatal(err)
	}

	preview := previewCollaboration(t, s, `{"version":4,"enabled":false}`)
	if preview.Policy.Enabled || preview.RoutingChanged || preview.ConfigChanged || len(preview.Artifacts) != 0 {
		t.Fatalf("disable preview unexpectedly depended on routing: %#v", preview)
	}
	body := `{"version":4,"enabled":false,"confirmed":true,"fingerprint":"` + preview.Fingerprint + `"}`
	response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", body)
	if response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}
	after, err := s.Collaboration.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled || after.ProviderID != before.ProviderID || after.Roles != before.Roles || after.DefaultTier != before.DefaultTier || len(after.ManagedArtifacts) != 9 {
		t.Fatalf("disabled policy did not preserve configuration while routing was unavailable: before=%#v after=%#v", before, after)
	}
}

func TestCollaborationStatusReportsMissingAndDriftedArtifacts(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	preview := previewCollaboration(t, s, request)
	applyBody := strings.TrimSuffix(request, "}") + `,"confirmed":true,"fingerprint":"` + preview.Fingerprint + `"}`
	response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", applyBody)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	statusResponse := invokeCollaboration(t, s.handleCollaboration, http.MethodGet, "/api/collaboration", "")
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	var status collaborationStatusDTO
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Configured || !status.Valid || status.Drifted {
		t.Fatalf("status = %#v", status)
	}

	policy, _ := s.Collaboration.Snapshot()
	if err := os.WriteFile(policy.ManagedArtifacts[0].Path, []byte("user-edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status = collaborationStatus(t, s)
	if status.Valid || !status.Drifted || len(status.Issues) == 0 {
		t.Fatalf("drift status = %#v", status)
	}
}

func TestCollaborationStatusAllowsNeverEnabledEmptyDisabledPolicy(t *testing.T) {
	s, _ := newCollaborationTestServer(t)
	if _, err := s.Collaboration.Replace(collaboration.DisabledPolicy()); err != nil {
		t.Fatal(err)
	}
	status := collaborationStatus(t, s)
	if !status.Configured || !status.Valid || status.Drifted || status.Policy == nil || status.Policy.Enabled {
		t.Fatalf("never-enabled disabled status = %#v", status)
	}
}

func TestCollaborationStatusRequiresExactCanonicalManifest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, policy *collaboration.Policy)
		want   string
	}{
		{
			name: "empty",
			mutate: func(_ *testing.T, policy *collaboration.Policy) {
				policy.ManagedArtifacts = nil
			},
			want: "必须恰好包含",
		},
		{
			name: "partial",
			mutate: func(_ *testing.T, policy *collaboration.Policy) {
				policy.ManagedArtifacts = policy.ManagedArtifacts[:len(policy.ManagedArtifacts)-1]
			},
			want: "必须恰好包含",
		},
		{
			name: "wrong path",
			mutate: func(t *testing.T, policy *collaboration.Policy) {
				content := []byte("relocated managed artifact\n")
				wrongPath := filepath.Join(t.TempDir(), "relocated.toml")
				if err := os.WriteFile(wrongPath, content, 0o600); err != nil {
					t.Fatal(err)
				}
				policy.ManagedArtifacts[0] = collaboration.ManagedArtifact{Path: wrongPath, SHA256: collaboration.Hash(content)}
			},
			want: "非 canonical 路径",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, request := newCollaborationTestServer(t)
			applyCollaborationRequest(t, s, request)
			policy, err := s.Collaboration.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, &policy)
			if _, err := s.Collaboration.Replace(policy); err != nil {
				t.Fatal(err)
			}

			status := collaborationStatus(t, s)
			if !status.Configured || status.Valid || !status.Drifted {
				t.Fatalf("manifest status = %#v", status)
			}
			if !strings.Contains(strings.Join(status.Issues, "\n"), tc.want) {
				t.Fatalf("manifest issues = %#v, want %q", status.Issues, tc.want)
			}
		})
	}
}

func TestCollaborationPreviewRejectsNoncanonicalPreviousManifest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*collaboration.Policy)
	}{
		{name: "partial", mutate: func(policy *collaboration.Policy) { policy.ManagedArtifacts = policy.ManagedArtifacts[:4] }},
		{name: "extra", mutate: func(policy *collaboration.Policy) {
			policy.ManagedArtifacts = append(policy.ManagedArtifacts, collaboration.ManagedArtifact{Path: filepath.Join(t.TempDir(), "extra.toml"), SHA256: strings.Repeat("a", 64)})
		}},
		{name: "relocated", mutate: func(policy *collaboration.Policy) {
			policy.ManagedArtifacts[0].Path = filepath.Join(t.TempDir(), "relocated.toml")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, request := newCollaborationTestServer(t)
			policy := applyCollaborationRequest(t, s, request)
			tc.mutate(&policy)
			if _, err := s.Collaboration.Replace(policy); err != nil {
				t.Fatal(err)
			}
			response := invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "previous collaboration manifest is not canonical") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCollaborationStatusRejectsSymlinkedManagedArtifact(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	applyCollaborationRequest(t, s, request)
	policy, err := s.Collaboration.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	managedPath := policy.ManagedArtifacts[0].Path
	original := readRequiredFile(t, managedPath)
	target := filepath.Join(t.TempDir(), "target.toml")
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(managedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, managedPath); err != nil {
		t.Fatal(err)
	}

	status := collaborationStatus(t, s)
	if status.Valid || !status.Drifted || !strings.Contains(strings.Join(status.Issues, "\n"), "不是普通文件") {
		t.Fatalf("symlink status = %#v", status)
	}
}

func TestCollaborationDisabledStatusStillReportsRetainedArtifactDrift(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	applyCollaborationRequest(t, s, request)
	disablePreview := previewCollaboration(t, s, `{"version":4,"enabled":false}`)
	disableBody := `{"version":4,"enabled":false,"confirmed":true,"fingerprint":"` + disablePreview.Fingerprint + `"}`
	if response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", disableBody); response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}
	policy, err := s.Collaboration.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy.ManagedArtifacts[0].Path, []byte("disabled user drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status := collaborationStatus(t, s)
	if !status.Configured || status.Valid || !status.Drifted || status.Policy == nil || status.Policy.Enabled {
		t.Fatalf("disabled drift status = %#v", status)
	}
	if !strings.Contains(strings.Join(status.Issues, "\n"), "受管文件已变更") {
		t.Fatalf("disabled drift issues = %#v", status.Issues)
	}
}

func TestCollaborationAPIIsLoopbackOnlyIncludingGET(t *testing.T) {
	s, _ := newCollaborationTestServer(t)
	for _, target := range []string{"/api/collaboration", "/api/collaboration/preview"} {
		req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:17878"+target, nil)
		req.RemoteAddr = "192.168.1.20:40000"
		if !loopbackOnlyRequest(req) {
			t.Fatalf("%s is not marked loopback-only", target)
		}
	}
	_ = s
}

func TestCollaborationApplyRollsBackArtifactsRoutingAndConfigWhenPolicyPersistenceFails(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	preview := previewCollaboration(t, s, request)
	beforeConfig := readRequiredFile(t, s.Switcher.ConfigPath)
	beforeRouting := readRequiredFile(t, s.Routing.Path())

	s.persistCollaboration = func(collaboration.Policy) (collaboration.Policy, error) {
		return collaboration.Policy{}, errors.New("injected policy persistence failure")
	}
	applyBody := strings.TrimSuffix(request, "}") + `,"confirmed":true,"fingerprint":"` + preview.Fingerprint + `"}`
	response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", applyBody)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	assertFileBytesEqual(t, s.Switcher.ConfigPath, beforeConfig)
	assertFileBytesEqual(t, s.Routing.Path(), beforeRouting)
	for _, artifact := range preview.Artifacts {
		if _, err := os.Stat(artifact.Path); !os.IsNotExist(err) {
			t.Fatalf("artifact remains after rollback %s: %v", artifact.Path, err)
		}
	}
}

func TestCollaborationDisabledStatusPreservesFourRolePayload(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	preview := previewCollaboration(t, s, request)
	applyBody := strings.TrimSuffix(request, "}") + `,"confirmed":true,"fingerprint":"` + preview.Fingerprint + `"}`
	if response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", applyBody); response.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.String())
	}
	before, err := s.Collaboration.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	disablePreview := previewCollaboration(t, s, `{"version":4,"enabled":false}`)
	disableBody := `{"version":4,"enabled":false,"confirmed":true,"fingerprint":"` + disablePreview.Fingerprint + `"}`
	if response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", disableBody); response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}

	response := invokeCollaboration(t, s.handleCollaboration, http.MethodGet, "/api/collaboration", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var status collaborationStatusDTO
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Configured || !status.Valid || status.Policy == nil || status.Policy.Enabled {
		t.Fatalf("disabled status = %#v", status)
	}
	if status.Policy.ProviderID != before.ProviderID || status.Policy.Roles != before.Roles || status.Policy.DefaultTier != before.DefaultTier || len(status.Policy.ManagedArtifacts) != 9 {
		t.Fatalf("disabled status lost four-role payload: before=%#v status=%#v", before, status.Policy)
	}
}

func TestCollaborationPreviewFailsClosedOnUntrustedSelectedEffortCapability(t *testing.T) {
	s, request := newCollaborationTestServer(t)
	items, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	profile := items[0]
	for i := range profile.Models {
		if profile.Models[i].Name == "subscription/codex/gpt-5.6-luna" {
			profile.Models[i].ReasoningEffortsSource = "unknown"
		}
	}
	if _, err := s.Profiles.Update(profile.ID, profile); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyCurrentRouting(); err != nil {
		t.Fatal(err)
	}
	response := invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", request)
	if response.Code != http.StatusBadRequest || !strings.Contains(strings.ToLower(response.Body.String()), "reasoning effort") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func newCollaborationTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	profile, err := profileStore.Create(profiles.Profile{
		Name:                   "Codex subscription",
		Source:                 "subscription-proxy:codex",
		BaseURL:                "https://codex.example/v1",
		APIKey:                 "secret",
		DefaultModel:           "subscription/codex/gpt-5.6-sol",
		DefaultReasoningEffort: "max",
		Models: []profiles.ModelDef{
			maxModel("subscription/codex/gpt-5.6-terra", profiles.SpeedTierStandard, "subscription/codex/gpt-5.6-terra"),
			maxModel("subscription/codex/gpt-5.6-terra-fast", profiles.SpeedTierFast, "subscription/codex/gpt-5.6-terra"),
			maxModel("subscription/codex/gpt-5.6-luna", profiles.SpeedTierStandard, "subscription/codex/gpt-5.6-luna"),
			maxModel("subscription/codex/gpt-5.6-luna-fast", profiles.SpeedTierFast, "subscription/codex/gpt-5.6-luna"),
			maxModel("subscription/codex/gpt-5.6-sol", profiles.SpeedTierStandard, "subscription/codex/gpt-5.6-sol"),
			maxModel("subscription/codex/gpt-5.6-sol-fast", profiles.SpeedTierFast, "subscription/codex/gpt-5.6-sol"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	routingPath := filepath.Join(dir, "routing.json")
	routingStore := routing.NewStore(routingPath)
	stored, err := routingStore.Initialize(profileStore)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "grok", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[features]\ntelemetry = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Paths:         pathsForCollaborationTest(dir, configPath),
		Profiles:      profileStore,
		Routing:       routingStore,
		Collaboration: collaboration.NewStore(filepath.Join(dir, "collaboration.json")),
		Switcher:      newSwitcherForCollaborationTest(configPath, profileStore),
	}
	list, _ := profileStore.List()
	hydrated, err := routing.ProjectWithSnapshot(list, stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Switcher.ApplyRouting(hydrated); err != nil {
		t.Fatal(err)
	}
	requestMap := map[string]any{
		"version":     4,
		"mode":        "single_provider",
		"enabled":     true,
		"provider_id": profile.ID,
		"roles": map[string]any{
			"main_coordinator": map[string]string{
				"provider_id": profile.ID, "data_scope": "repository_plus_minimized_prior_work_products", "model": profile.ID + ":subscription/codex/gpt-5.6-terra", "speed_tier": collaboration.SpeedTierFast, "reasoning_effort": "high",
			},
			"task_decomposition": map[string]string{
				"provider_id": profile.ID, "data_scope": "repository_only", "model": profile.ID + ":subscription/codex/gpt-5.6-luna", "speed_tier": collaboration.SpeedTierStandard, "reasoning_effort": "medium",
			},
			"main_implementation": map[string]string{
				"provider_id": profile.ID, "data_scope": "repository_plus_minimized_prior_work_products", "model": profile.ID + ":subscription/codex/gpt-5.6-terra", "speed_tier": collaboration.SpeedTierFast, "reasoning_effort": "xhigh",
			},
			"difficult_implementation_review": map[string]string{
				"provider_id": profile.ID, "data_scope": "repository_plus_minimized_prior_work_products", "model": profile.ID + ":subscription/codex/gpt-5.6-sol", "speed_tier": collaboration.SpeedTierFast, "reasoning_effort": "max",
			},
		},
		"default_tier": "adaptive",
	}
	raw, err := json.Marshal(requestMap)
	if err != nil {
		t.Fatal(err)
	}
	return s, string(raw)
}

func maxModel(name, speedTier, standardAnchor string) profiles.ModelDef {
	return profiles.ModelDef{
		Name: name, Model: name, APIBackend: "chat_completions",
		SupportsReasoningEffort: true,
		ReasoningEfforts:        []string{"low", "medium", "high", "xhigh", "max"},
		ReasoningEffortsSource:  "declared",
		SpeedTier:               speedTier,
		StandardAnchor:          standardAnchor,
	}
}

func invokeCollaboration(t *testing.T, handler http.HandlerFunc, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler(response, loopbackRequest(method, target, body))
	return response
}

func previewCollaboration(t *testing.T, s *Server, request string) collaborationPreviewDTO {
	t.Helper()
	response := invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", request)
	if response.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
	}
	var preview collaborationPreviewDTO
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	return preview
}

func applyCollaborationRequest(t *testing.T, s *Server, request string) collaboration.Policy {
	t.Helper()
	preview := previewCollaboration(t, s, request)
	applyBody := strings.TrimSuffix(request, "}") + `,"confirmed":true,"fingerprint":"` + preview.Fingerprint + `"}`
	response := invokeCollaboration(t, s.handleCollaboration, http.MethodPut, "/api/collaboration", applyBody)
	if response.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.String())
	}
	policy, err := s.Collaboration.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func collaborationStatus(t *testing.T, s *Server) collaborationStatusDTO {
	t.Helper()
	response := invokeCollaboration(t, s.handleCollaboration, http.MethodGet, "/api/collaboration", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var status collaborationStatusDTO
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func decodeCollaborationRequestMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func encodeCollaborationRequestMap(t *testing.T, value map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func readRequiredFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// These tiny constructors keep the test fixture explicit without teaching the
// production server about tests.
func pathsForCollaborationTest(dir, configPath string) paths.Paths {
	return paths.Paths{GrokConfig: configPath, GrokHome: filepath.Join(dir, "grok"), DataDir: dir, CollaborationFile: filepath.Join(dir, "collaboration.json")}
}

func newSwitcherForCollaborationTest(configPath string, store *profiles.Store) *switcher.Switcher {
	return &switcher.Switcher{ConfigPath: configPath, Profiles: store}
}

func TestCollaborationPreviewRejectsTamperedWorkflowDerivedDataScope(t *testing.T) {
	s, raw := newCollaborationTestServer(t)
	m := decodeCollaborationRequestMap(t, raw)
	m["roles"].(map[string]any)["task_decomposition"].(map[string]any)["data_scope"] = collaboration.DataScopePriorWork
	resp := invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", encodeCollaborationRequestMap(t, m))
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "task decomposition data scope") {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestCollaborationStatusReportsFederatedStructuralBlockerBeforeRouteHealth(t *testing.T) {
	s, raw := newCollaborationTestServer(t)
	profile, err := s.Profiles.Create(profiles.Profile{Name: "Inactive Profile", Source: "custom", BaseURL: "https://inactive.example/v1", APIKey: "inactive-secret", DefaultModel: "reasoner", DefaultReasoningEffort: "high", Models: []profiles.ModelDef{{Name: "reasoner", Model: "reasoner", SpeedTier: profiles.SpeedTierStandard, StandardAnchor: "reasoner", SupportsReasoningEffort: true, ReasoningEfforts: []string{"high"}, ReasoningEffortsSource: "declared"}}})
	if err != nil {
		t.Fatal(err)
	}
	m := decodeCollaborationRequestMap(t, raw)
	m["mode"] = collaboration.ModeFederated
	m["provider_id"] = "deliberately-not-active"
	roles := m["roles"].(map[string]any)
	roles["task_decomposition"].(map[string]any)["provider_id"] = profile.ID
	roles["task_decomposition"].(map[string]any)["model"] = profile.ID + ":reasoner"
	roles["task_decomposition"].(map[string]any)["reasoning_effort"] = "high"
	policy := collaboration.NewPolicy("deliberately-not-active", collaboration.RoleAssignments{
		MainCoordinator:               collaboration.RoleAssignment{ProviderID: "deliberately-not-active", Model: "missing", SpeedTier: "standard", ReasoningEffort: "high", DataScope: collaboration.DataScopePriorWork},
		TaskDecomposition:             collaboration.RoleAssignment{ProviderID: profile.ID, Model: profile.ID + ":reasoner", SpeedTier: "standard", ReasoningEffort: "high", DataScope: collaboration.DataScopeRepositoryOnly},
		MainImplementation:            collaboration.RoleAssignment{ProviderID: "deliberately-not-active", Model: "missing", SpeedTier: "standard", ReasoningEffort: "high", DataScope: collaboration.DataScopePriorWork},
		DifficultImplementationReview: collaboration.RoleAssignment{ProviderID: "deliberately-not-active", Model: "missing", SpeedTier: "standard", ReasoningEffort: "high", DataScope: collaboration.DataScopePriorWork},
	})
	policy.Mode = collaboration.ModeFederated
	policy.FederationConsent = &collaboration.FederationConsent{Basis: collaboration.FederationConsentBasisAllWorkflowTiersV1, ProviderIDs: policy.CanonicalProviderIDs(), HandoffPolicy: collaboration.HandoffPolicyBounded, TierHandoffEdges: policy.CanonicalTierHandoffEdges(), NeverTransfer: []string{collaboration.NeverTransferCredentials, collaboration.NeverTransferSecrets, collaboration.NeverTransferTranscripts}}
	if _, err := s.Collaboration.Replace(policy); err != nil {
		t.Fatal(err)
	}
	status := collaborationStatus(t, s)
	if status.Valid || len(status.Issues) == 0 || status.Issues[len(status.Issues)-1] != federatedStructuralBlocker {
		t.Fatalf("federated status = %#v", status)
	}
	joined := strings.Join(status.Issues, "\n")
	for _, misleading := range []string{"collaboration coordinator provider", "config.toml 与 collaboration 路由不一致", "重建路由失败"} {
		if strings.Contains(joined, misleading) {
			t.Fatalf("federated status appended misleading route/config issue %q: %#v", misleading, status.Issues)
		}
	}
}

func TestCollaborationSpecExposesVersionedCanonicalWorkflowContract(t *testing.T) {
	s, _ := newCollaborationTestServer(t)
	response := invokeCollaboration(t, s.handleCollaborationSpec, http.MethodGet, "/api/collaboration/spec", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if got := response.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma=%q", got)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 3 || raw["schema_version"] == nil || raw["collaboration_policy_version"] == nil || raw["workflow_paths"] == nil {
		t.Fatalf("unexpected exact payload fields: %s", response.Body.String())
	}
	var payload collaborationSpecDTO
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SchemaVersion != collaborationSpecSchemaVersion || payload.CollaborationPolicyVersion != collaboration.CurrentVersion {
		t.Fatalf("versions=%#v", payload)
	}
	want := collaboration.WorkflowPaths()
	if !reflect.DeepEqual(payload.WorkflowPaths, want) {
		t.Fatalf("workflow spec=%#v want=%#v", payload.WorkflowPaths, want)
	}
	wantJSON, err := json.Marshal(collaborationSpecDTO{SchemaVersion: collaborationSpecSchemaVersion, CollaborationPolicyVersion: collaboration.CurrentVersion, WorkflowPaths: want})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(response.Body.String()) != string(wantJSON) {
		t.Fatalf("payload=%q want=%q", strings.TrimSpace(response.Body.String()), wantJSON)
	}
}

func TestCollaborationSpecRemainsGETOnlyAndLoopbackOnly(t *testing.T) {
	s, _ := newCollaborationTestServer(t)
	if response := invokeCollaboration(t, s.handleCollaborationSpec, http.MethodPost, "/api/collaboration/spec", `{}`); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d body=%s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/collaboration/spec", nil)
	request.RemoteAddr = "192.0.2.10:12345"
	response := httptest.NewRecorder()
	s.handleCollaborationSpec(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-loopback status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCollaborationFederatedPreviewFailsClosedOnActiveProviderArchitecture(t *testing.T) {
	s, raw := newCollaborationTestServer(t)
	profile, err := s.Profiles.Create(profiles.Profile{Name: "DeepSeek", Source: "custom", BaseURL: "https://deepseek.example/v1", APIKey: "deepseek-secret", DefaultModel: "deepseek-reasoner", DefaultReasoningEffort: "high", Models: []profiles.ModelDef{{Name: "deepseek-reasoner", Model: "deepseek-reasoner", SpeedTier: profiles.SpeedTierStandard, StandardAnchor: "deepseek-reasoner", SupportsReasoningEffort: true, ReasoningEfforts: []string{"high"}, ReasoningEffortsSource: "declared"}}})
	if err != nil {
		t.Fatal(err)
	}
	m := decodeCollaborationRequestMap(t, raw)
	m["mode"] = "federated"
	roles := m["roles"].(map[string]any)
	td := roles["task_decomposition"].(map[string]any)
	td["provider_id"] = profile.ID
	td["model"] = profile.ID + ":deepseek-reasoner"
	td["speed_tier"] = "standard"
	td["reasoning_effort"] = "high"
	ids := []string{profile.ID, m["provider_id"].(string)}
	if ids[0] > ids[1] {
		ids[0], ids[1] = ids[1], ids[0]
	}
	m["federation_consent"] = map[string]any{"provider_ids": ids, "handoff_policy": "bounded_work_products", "basis": "all_workflow_tiers_v1", "tier_handoff_edges": []map[string]any{{"tier": "economy", "edges": []map[string]string{}}, {"tier": "focused-evidence", "edges": []map[string]string{{"from": "task_decomposition", "to": "main_coordinator"}}}, {"tier": "focused-build", "edges": []map[string]string{}}, {"tier": "assurance", "edges": []map[string]string{{"from": "task_decomposition", "to": "main_implementation"}, {"from": "task_decomposition", "to": "main_coordinator"}}}, {"tier": "critical", "edges": []map[string]string{{"from": "task_decomposition", "to": "main_implementation"}, {"from": "task_decomposition", "to": "difficult_implementation_review"}, {"from": "task_decomposition", "to": "main_coordinator"}}}}, "never_transfer": []string{"credentials", "secrets", "full_transcripts"}}
	resp := invokeCollaboration(t, s.handleCollaborationPreview, http.MethodPost, "/api/collaboration/preview", encodeCollaborationRequestMap(t, m))
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "structurally blocked") {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "deepseek-secret") {
		t.Fatal("credential leaked in blocker")
	}
}
