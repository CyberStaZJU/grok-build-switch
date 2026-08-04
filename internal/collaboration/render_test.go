package collaboration

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderArtifactsDeterministicWithResolvedStandardFastModelsAndEfforts(t *testing.T) {
	root := t.TempDir()
	paths := ArtifactPathsForGrokHome(root)
	policy := NewPolicy("provider-1", testRoleAssignments())
	first, err := RenderArtifacts(policy, capableSnapshot(), paths)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderArtifacts(policy, capableSnapshot(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 9 || len(second) != 9 {
		t.Fatalf("artifact counts = %d, %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Path != second[i].Path || !bytes.Equal(first[i].Content, second[i].Content) || first[i].SHA256 != second[i].SHA256 {
			t.Fatalf("artifact %d was not deterministic\nfirst=%#v\nsecond=%#v", i, first[i], second[i])
		}
		if first[i].SHA256 != Hash(first[i].Content) {
			t.Fatalf("artifact %s hash mismatch", first[i].Path)
		}
	}

	roles := map[string]string{}
	agents := map[string]string{}
	for _, artifact := range first {
		switch filepath.Ext(artifact.Path) {
		case ".toml":
			roles[filepath.Base(artifact.Path)] = string(artifact.Content)
		case ".md":
			agents[filepath.Base(artifact.Path)] = string(artifact.Content)
		}
	}
	assertRole(t, roles[MainCoordinatorRoleName+".toml"], "subscription/codex/gpt-5.6-terra", "high", "all")
	assertRole(t, roles[TaskDecompositionRoleName+".toml"], "subscription/codex/gpt-5.6-luna", "medium", "read-only")
	assertRole(t, roles[MainImplementationRoleName+".toml"], "subscription/codex/gpt-5.6-terra-fast", "xhigh", "all")
	assertRole(t, roles[DifficultReviewRoleName+".toml"], "subscription/codex/gpt-5.6-sol-fast", "max", "all")
	assertAgentDefinition(t, agents[MainCoordinatorRoleName+".md"], MainCoordinatorRoleName, "default")
	assertAgentDefinition(t, agents[TaskDecompositionRoleName+".md"], TaskDecompositionRoleName, "plan")
	assertAgentDefinition(t, agents[MainImplementationRoleName+".md"], MainImplementationRoleName, "default")
	assertAgentDefinition(t, agents[DifficultReviewRoleName+".md"], DifficultReviewRoleName, "default")

	workflow := string(first[len(first)-1].Content)
	for _, want := range []string{
		`name: "gbs-max-collab"`,
		`agent_type: "gbs-luna-evidence"`,
		`agent_type: "gbs-main-implementation"`,
		`agent_type: "gbs-sol-builder"`,
		`agent_type: "gbs-terra-coordinator"`,
		`model: "subscription/codex/gpt-5.6-luna"`,
		`model: "subscription/codex/gpt-5.6-sol-fast"`,
		`model: "subscription/codex/gpt-5.6-terra-fast"`,
		`model: "subscription/codex/gpt-5.6-terra"`,
		`if run_budget.total != expected_budget`,
		`Do not launch this named workflow directly from slash autocomplete`,
		`Ask Grok in natural language`,
		`args.objective, args.tier, and the matching agent_budget`,
		`focused-evidence`,
		`focused-build`,
		`assurance`,
		`critical`,
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow missing %q:\n%s", want, workflow)
		}
	}
	for _, forbidden := range []string{"parallel(", "fork_context", "resume_from", "reasoning_effort", "speed_tier", "standard_anchor", "service_tier", "provider-1:"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("workflow unexpectedly contains %q:\n%s", forbidden, workflow)
		}
	}
	decompose := strings.Index(workflow, `agent_type: "gbs-luna-evidence"`)
	implement := strings.Index(workflow, `agent_type: "gbs-main-implementation"`)
	review := strings.Index(workflow, `agent_type: "gbs-sol-builder"`)
	coordinate := strings.LastIndex(workflow, `agent_type: "gbs-terra-coordinator"`)
	if decompose < 0 || implement < 0 || review < 0 || coordinate < 0 || !(decompose < implement && implement < review && review < coordinate) {
		t.Fatalf("critical order is not decomposition -> implementation -> difficult review -> coordinator:\n%s", workflow)
	}
}

func TestRenderedWorkflowTierPathsUseExpectedRolesAndBudgets(t *testing.T) {
	workflow := string(renderWorkflow(
		"subscription/codex/gpt-5.6-terra",
		"subscription/codex/gpt-5.6-luna",
		"subscription/codex/gpt-5.6-terra-fast",
		"subscription/codex/gpt-5.6-sol-fast",
	))

	for _, want := range []string{
		`if tier == "economy" { expected_budget = 1; }`,
		`else if tier == "focused-evidence" { expected_budget = 2; }`,
		`else if tier == "focused-build" { expected_budget = 2; }`,
		`else if tier == "assurance" { expected_budget = 3; }`,
		`else if tier == "critical" { expected_budget = 4; }`,
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("workflow missing budget guard %q:\n%s", want, workflow)
		}
	}

	decomposeBlock := workflowSection(t, workflow,
		`if tier == "focused-evidence" || tier == "assurance" || tier == "critical" {`,
		`if tier == "focused-build" || tier == "assurance" || tier == "critical" {`)
	implementBlock := workflowSection(t, workflow,
		`if tier == "focused-build" || tier == "assurance" || tier == "critical" {`,
		`if tier == "critical" {`)
	reviewBlock := workflowSection(t, workflow,
		"if tier == \"critical\" {\n    phase(\"Review\");",
		`phase("Coordinate");`)
	coordinateBlock := workflow[strings.Index(workflow, `phase("Coordinate");`):]

	assertWorkflowSectionAgents(t, decomposeBlock, []string{TaskDecompositionRoleName})
	assertWorkflowSectionAgents(t, implementBlock, []string{MainImplementationRoleName})
	assertWorkflowSectionAgents(t, reviewBlock, []string{DifficultReviewRoleName})
	assertWorkflowSectionAgents(t, coordinateBlock, []string{MainCoordinatorRoleName})
}

func workflowSection(t *testing.T, workflow, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(workflow, startMarker)
	if start < 0 {
		t.Fatalf("workflow missing section start %q:\n%s", startMarker, workflow)
	}
	endOffset := strings.Index(workflow[start+len(startMarker):], endMarker)
	if endOffset < 0 {
		t.Fatalf("workflow missing section end %q after %q:\n%s", endMarker, startMarker, workflow)
	}
	return workflow[start : start+len(startMarker)+endOffset]
}

func assertWorkflowSectionAgents(t *testing.T, section string, want []string) {
	t.Helper()
	allRoles := []string{MainCoordinatorRoleName, TaskDecompositionRoleName, MainImplementationRoleName, DifficultReviewRoleName}
	for _, role := range allRoles {
		hasRole := strings.Contains(section, `agent_type: "`+role+`"`)
		wantRole := false
		for _, expected := range want {
			if role == expected {
				wantRole = true
				break
			}
		}
		if hasRole != wantRole {
			t.Fatalf("section role %q present=%v, want=%v:\n%s", role, hasRole, wantRole, section)
		}
	}
}

func TestRenderedWorkflowDataForwardingMatchesCanonicalSpecExactly(t *testing.T) {
	workflow := string(renderWorkflow("coordinator", "decomposition", "implementation", "review"))
	forwarding := map[HandoffEdge]string{
		{From: RoleTaskDecomposition, To: RoleMainImplementation}:             `if decomposition_packet != "" { implementation_prompt +=`,
		{From: RoleTaskDecomposition, To: RoleDifficultImplementationReview}:  `if decomposition_packet != "" { review_prompt +=`,
		{From: RoleMainImplementation, To: RoleDifficultImplementationReview}: `if implementation_packet != "" { review_prompt +=`,
		{From: RoleTaskDecomposition, To: RoleMainCoordinator}:                `if decomposition_packet != "" { coordinate_prompt +=`,
		{From: RoleMainImplementation, To: RoleMainCoordinator}:               `if implementation_packet != "" { coordinate_prompt +=`,
		{From: RoleDifficultImplementationReview, To: RoleMainCoordinator}:    `if review_packet != "" { coordinate_prompt +=`,
	}
	canonical := map[HandoffEdge]bool{}
	for _, path := range WorkflowPaths() {
		for _, flow := range path.DataFlows {
			canonical[flow] = true
		}
	}
	if len(canonical) != len(forwarding) {
		t.Fatalf("canonical unique flows=%d forwarding markers=%d", len(canonical), len(forwarding))
	}
	for flow, marker := range forwarding {
		if !canonical[flow] {
			t.Fatalf("renderer forwards non-canonical flow %#v", flow)
		}
		if count := strings.Count(workflow, marker); count != 1 {
			t.Fatalf("flow %#v marker count=%d, want 1\n%s", flow, count, workflow)
		}
	}
	for _, forbidden := range []string{"transcript_packet", "credential_packet", "secret_packet", "api_key", "full_transcript"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("workflow contains hidden transfer variable %q", forbidden)
		}
	}
}

func TestValidateCanonicalManifestAcceptsExactLegacyFiveFileUpgradeSource(t *testing.T) {
	paths := ArtifactPathsForGrokHome(t.TempDir())
	manifest := make([]ManagedArtifact, 0, len(paths.LegacyCanonicalPaths()))
	for _, path := range paths.LegacyCanonicalPaths() {
		manifest = append(manifest, ManagedArtifact{Path: path, SHA256: strings.Repeat("a", 64)})
	}
	if err := ValidateCanonicalManifest(manifest, paths); err != nil {
		t.Fatalf("legacy manifest should be accepted for upgrade: %v", err)
	}
	manifest = manifest[:4]
	if err := ValidateCanonicalManifest(manifest, paths); err == nil {
		t.Fatal("partial legacy manifest was accepted")
	}
}

func TestRenderArtifactsRequiresExpectedManagedPaths(t *testing.T) {
	policy := NewPolicy("provider-1", testRoleAssignments())
	_, err := RenderArtifacts(policy, capableSnapshot(), ArtifactPaths{})
	if err == nil || !strings.Contains(err.Error(), "artifact path") {
		t.Fatalf("RenderArtifacts() error = %v", err)
	}
}

func TestRenderArtifactsUsesResolvedConcreteAliasesWithoutInternalMetadata(t *testing.T) {
	snapshot := capableSnapshot()
	policy := NewPolicy("provider-1", testRoleAssignments())
	paths := ArtifactPathsForGrokHome(t.TempDir())
	artifacts, err := RenderArtifacts(policy, snapshot, paths)
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, artifact := range artifacts {
		joined += string(artifact.Content)
	}
	for _, model := range []string{
		"subscription/codex/gpt-5.6-terra",
		"subscription/codex/gpt-5.6-luna",
		"subscription/codex/gpt-5.6-terra-fast",
		"subscription/codex/gpt-5.6-sol-fast",
	} {
		if !strings.Contains(joined, model) {
			t.Fatalf("rendered artifacts do not contain resolved model %q", model)
		}
	}
	for _, forbidden := range []string{"provider-1:", "speed_tier", "standard_anchor", "service_tier"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("rendered artifacts leaked internal metadata %q", forbidden)
		}
	}
}

func TestRenderArtifactsFailsClosedWhenFastPartnerDisappears(t *testing.T) {
	snapshot := capableSnapshot()
	removeTestRoute(&snapshot, "provider-1:terra-fast")
	_, err := RenderArtifacts(NewPolicy("provider-1", testRoleAssignments()), snapshot, ArtifactPathsForGrokHome(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "refusing to fall back") {
		t.Fatalf("RenderArtifacts() error = %v", err)
	}
}

func assertRole(t *testing.T, content, model, effort, capability string) {
	t.Helper()
	for _, want := range []string{
		`model = "` + model + `"`,
		`reasoning_effort = "` + effort + `"`,
		`default_capability_mode = "` + capability + `"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("role missing %q:\n%s", want, content)
		}
	}
	for _, forbidden := range []string{"default_fork_context", "speed_tier", "standard_anchor", "service_tier", "provider-1:"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("role should not contain %q:\n%s", forbidden, content)
		}
	}
}

func assertAgentDefinition(t *testing.T, content, name, permissionMode string) {
	t.Helper()
	for _, want := range []string{
		"---\n",
		"name: " + name + "\n",
		"model: inherit\n",
		"permission_mode: " + permissionMode + "\n",
		"agents_md: true\n",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("agent definition missing %q:\n%s", want, content)
		}
	}
	for _, forbidden := range []string{"reasoning_effort", "subscription/codex/", "service_tier", "provider-1:"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("agent definition should not contain %q:\n%s", forbidden, content)
		}
	}
}
