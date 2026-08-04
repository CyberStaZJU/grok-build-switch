package collaboration

import (
	"fmt"
	"strings"
	"testing"

	"grok_switch/internal/modelvariants"
	"grok_switch/internal/routing"
)

func TestNewPolicyValidatesFourRolesWithIndependentSpeedsAndEfforts(t *testing.T) {
	snapshot := capableSnapshot()
	policy := NewPolicy("provider-1", testRoleAssignments())

	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	resolved, err := policy.ResolveAgainstRouting(snapshot)
	if err != nil {
		t.Fatalf("ResolveAgainstRouting() error = %v", err)
	}
	if policy.Version != CurrentVersion || !policy.Enabled {
		t.Fatalf("policy defaults = %#v", policy)
	}
	if policy.DefaultTier != DefaultTierAdaptive || policy.MaxParallel != 1 || policy.RetryLimit != 1 {
		t.Fatalf("policy controls = %#v", policy)
	}
	if policy.Budgets != DefaultTierBudgets() {
		t.Fatalf("budgets = %#v, want %#v", policy.Budgets, DefaultTierBudgets())
	}
	if policy.Roles.MainCoordinator.Model != policy.Roles.MainImplementation.Model {
		t.Fatalf("duplicate Standard anchor was not retained: %#v", policy.Roles)
	}
	if policy.Roles.MainCoordinator.SpeedTier != SpeedTierStandard || policy.Roles.MainImplementation.SpeedTier != SpeedTierFast {
		t.Fatalf("independent speeds were not retained: %#v", policy.Roles)
	}
	if resolved.MainCoordinator.Route.ID != "provider-1:terra" || resolved.MainImplementation.Route.ID != "provider-1:terra-fast" {
		t.Fatalf("resolved roles = %#v", resolved)
	}
}

func TestPolicyRejectsInvalidStructuralControls(t *testing.T) {
	base := NewPolicy("provider-1", testRoleAssignments())
	tests := []struct {
		name   string
		mutate func(*Policy)
		want   string
	}{
		{name: "version", mutate: func(p *Policy) { p.Version = 99 }, want: "version"},
		{name: "missing model", mutate: func(p *Policy) { p.Roles.MainImplementation.Model = "" }, want: "main implementation"},
		{name: "missing speed", mutate: func(p *Policy) { p.Roles.MainCoordinator.SpeedTier = "" }, want: "speed tier"},
		{name: "invalid speed", mutate: func(p *Policy) { p.Roles.MainCoordinator.SpeedTier = "turbo" }, want: "speed tier"},
		{name: "unnormalized speed", mutate: func(p *Policy) { p.Roles.MainCoordinator.SpeedTier = " Fast " }, want: "speed tier"},
		{name: "invalid effort", mutate: func(p *Policy) { p.Roles.TaskDecomposition.ReasoningEffort = "ultra" }, want: "reasoning effort"},
		{name: "unnormalized effort", mutate: func(p *Policy) { p.Roles.TaskDecomposition.ReasoningEffort = " High " }, want: "reasoning effort"},
		{name: "scope", mutate: func(p *Policy) { p.ArtifactScope = "project" }, want: "user"},
		{name: "parallel", mutate: func(p *Policy) { p.MaxParallel = 2 }, want: "max_parallel"},
		{name: "retry", mutate: func(p *Policy) { p.RetryLimit = 2 }, want: "retry_limit"},
		{name: "tier", mutate: func(p *Policy) { p.DefaultTier = "automatic" }, want: "default tier"},
		{name: "budget", mutate: func(p *Policy) { p.Budgets.Assurance = 4 }, want: "assurance"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := base
			test.mutate(&policy)
			err := policy.Validate()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestPolicyAllowsSameStandardAnchorAcrossRolesWithDifferentSpeeds(t *testing.T) {
	roles := testRoleAssignments()
	roles.TaskDecomposition.Model = roles.MainCoordinator.Model
	roles.TaskDecomposition.SpeedTier = SpeedTierFast
	roles.TaskDecomposition.ReasoningEffort = "medium"
	roles.DifficultImplementationReview.Model = roles.MainCoordinator.Model
	roles.DifficultImplementationReview.SpeedTier = SpeedTierStandard
	roles.DifficultImplementationReview.ReasoningEffort = "max"
	policy := NewPolicy("provider-1", roles)
	resolved, err := policy.ResolveAgainstRouting(capableSnapshot())
	if err != nil {
		t.Fatalf("duplicate role anchors should be valid: %v", err)
	}
	if resolved.TaskDecomposition.Route.ID != "provider-1:terra-fast" || resolved.DifficultImplementationReview.Route.ID != "provider-1:terra" {
		t.Fatalf("resolved duplicate anchors = %#v", resolved)
	}
}

func TestPolicyRejectsRoutesOutsideSelectedProvider(t *testing.T) {
	snapshot := capableSnapshot()
	snapshot.Providers = append(snapshot.Providers, routing.Provider{ID: "provider-2", Name: "Other", ProfileID: "profile-2", Source: "subscription-proxy:codex"})
	snapshot.ModelRoutes = append(snapshot.ModelRoutes, trustedRoute("provider-2:other", "subscription/codex/gpt-5.6-sol", "provider-2", SpeedTierStandard, "provider-2:other"))
	snapshot.ProviderPolicies["provider-2"] = routing.RoutingPolicy{Default: "provider-2:other"}

	roles := testRoleAssignments()
	roles.DifficultImplementationReview.Model = "provider-2:other"
	roles.DifficultImplementationReview.SpeedTier = SpeedTierStandard
	policy := NewPolicy("provider-1", roles)
	err := policy.ValidateAgainstRouting(snapshot)
	if err == nil || !strings.Contains(err.Error(), "provider-2") {
		t.Fatalf("ValidateAgainstRouting() error = %v", err)
	}
}

func TestPolicyRejectsInactiveOrOfficialProvider(t *testing.T) {
	snapshot := capableSnapshot()
	policy := NewPolicy("provider-1", testRoleAssignments())

	snapshot.ActiveProviderID = "other"
	if err := policy.ValidateAgainstRouting(snapshot); err == nil || !strings.Contains(err.Error(), "active provider") {
		t.Fatalf("inactive provider error = %v", err)
	}

	snapshot.ActiveProviderID = routing.OfficialProviderID
	policy.ProviderID = routing.OfficialProviderID
	if err := policy.ValidateAgainstRouting(snapshot); err == nil || !strings.Contains(strings.ToLower(err.Error()), "official") {
		t.Fatalf("official provider error = %v", err)
	}
}

func TestPolicyFailsClosedWhenResolvedEffortCapabilityIsUnknown(t *testing.T) {
	base := capableSnapshot()
	tests := []struct {
		name   string
		mutate func(*routing.ModelRoute)
	}{
		{name: "missing selected effort", mutate: func(route *routing.ModelRoute) { route.ReasoningEfforts = []string{"low", "high", "max"} }},
		{name: "unknown source", mutate: func(route *routing.ModelRoute) { route.ReasoningEffortsSource = "unknown" }},
		{name: "default source", mutate: func(route *routing.ModelRoute) { route.ReasoningEffortsSource = "default" }},
		{name: "support false", mutate: func(route *routing.ModelRoute) { route.SupportsReasoningEffort = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			snapshot.ModelRoutes = append([]routing.ModelRoute(nil), base.ModelRoutes...)
			route := routeByTestID(t, &snapshot, "provider-1:terra-fast")
			test.mutate(route)
			policy := NewPolicy("provider-1", testRoleAssignments())
			err := policy.ValidateAgainstRouting(snapshot)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "reasoning effort") {
				t.Fatalf("ValidateAgainstRouting() error = %v", err)
			}
		})
	}
}

func TestSingleProviderStandardResolutionRequiresTrustedCodexProviderAndRegistry(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*routing.Snapshot)
		want   string
	}{
		{
			name: "untrusted provider source",
			mutate: func(snapshot *routing.Snapshot) {
				snapshot.Providers[0].Source = "custom"
			},
			want: "subscription-proxy:codex",
		},
		{
			name: "standard route outside registry",
			mutate: func(snapshot *routing.Snapshot) {
				route := routeByTestID(t, snapshot, "provider-1:terra")
				route.ProfileModel = "subscription/codex/not-trusted"
				route.Model = route.ProfileModel
			},
			want: "exact trusted model registry",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := capableSnapshot()
			roles := testRoleAssignments()
			roles.MainCoordinator.SpeedTier = SpeedTierStandard
			roles.TaskDecomposition.SpeedTier = SpeedTierStandard
			roles.MainImplementation.SpeedTier = SpeedTierStandard
			roles.DifficultImplementationReview.SpeedTier = SpeedTierStandard
			test.mutate(&snapshot)
			err := NewPolicy("provider-1", roles).ValidateAgainstRouting(snapshot)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("ValidateAgainstRouting() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPolicyFastResolutionFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*routing.Snapshot, *Policy)
		want   string
	}{
		{
			name: "stored model is concrete fast route",
			mutate: func(_ *routing.Snapshot, policy *Policy) {
				policy.Roles.MainImplementation.Model = "provider-1:terra-fast"
				policy.Roles.MainImplementation.SpeedTier = SpeedTierStandard
			},
			want: "not an explicit self-anchored Standard route",
		},
		{
			name: "missing fast partner",
			mutate: func(snapshot *routing.Snapshot, _ *Policy) {
				removeTestRoute(snapshot, "provider-1:terra-fast")
			},
			want: "refusing to fall back",
		},
		{
			name: "ambiguous fast partner",
			mutate: func(snapshot *routing.Snapshot, _ *Policy) {
				duplicate := *routeByTestID(t, snapshot, "provider-1:terra-fast")
				duplicate.ID = "provider-1:terra-fast-duplicate"
				duplicate.Name = "duplicate-fast"
				snapshot.ModelRoutes = append(snapshot.ModelRoutes, duplicate)
			},
			want: "ambiguous",
		},
		{
			name: "forged fast alias",
			mutate: func(snapshot *routing.Snapshot, _ *Policy) {
				route := routeByTestID(t, snapshot, "provider-1:terra-fast")
				route.ProfileModel = "subscription/codex/gpt-5.6-terra-fast-forged"
				route.Model = route.ProfileModel
			},
			want: "exact trusted Fast alias",
		},
		{
			name: "untrusted provider source",
			mutate: func(snapshot *routing.Snapshot, _ *Policy) {
				snapshot.Providers[0].Source = "subscription"
			},
			want: "subscription-proxy:codex",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := capableSnapshot()
			policy := NewPolicy("provider-1", testRoleAssignments())
			test.mutate(&snapshot, &policy)
			err := policy.ValidateAgainstRouting(snapshot)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("ValidateAgainstRouting() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPolicyValidatesEffortOnSelectedConcreteTier(t *testing.T) {
	snapshot := capableSnapshot()
	standard := routeByTestID(t, &snapshot, "provider-1:terra")
	standard.ReasoningEfforts = []string{"high"}
	fast := routeByTestID(t, &snapshot, "provider-1:terra-fast")
	fast.ReasoningEfforts = []string{"xhigh"}

	roles := testRoleAssignments()
	roles.MainCoordinator.SpeedTier = SpeedTierStandard
	roles.MainCoordinator.ReasoningEffort = "high"
	roles.MainImplementation.SpeedTier = SpeedTierFast
	roles.MainImplementation.ReasoningEffort = "xhigh"
	if err := NewPolicy("provider-1", roles).ValidateAgainstRouting(snapshot); err != nil {
		t.Fatalf("concrete-tier efforts should validate: %v", err)
	}

	roles.MainImplementation.ReasoningEffort = "high"
	if err := NewPolicy("provider-1", roles).ValidateAgainstRouting(snapshot); err == nil || !strings.Contains(err.Error(), "reasoning effort") {
		t.Fatalf("Fast route accepted Standard-only effort: %v", err)
	}
}

func TestDisabledPolicyValidatesWithoutRoutes(t *testing.T) {
	policy := DisabledPolicy()
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := policy.ValidateAgainstRouting(routing.Snapshot{}); err != nil {
		t.Fatalf("ValidateAgainstRouting() error = %v", err)
	}
}

func TestPolicyManifestValidation(t *testing.T) {
	policy := NewPolicy("provider-1", testRoleAssignments())
	policy.ManagedArtifacts = []ManagedArtifact{
		{Path: "/tmp/a", SHA256: strings.Repeat("a", 64)},
		{Path: "/tmp/b", SHA256: strings.Repeat("b", 64)},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	policy.ManagedArtifacts[1].Path = "/tmp/a"
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate manifest error = %v", err)
	}
	policy.ManagedArtifacts[1].Path = "/tmp/b"
	policy.ManagedArtifacts[1].SHA256 = "not-a-hash"
	if err := policy.Validate(); err == nil || !strings.Contains(strings.ToLower(err.Error()), "sha-256") {
		t.Fatalf("hash error = %v", err)
	}
}

func testRoleAssignments() RoleAssignments {
	return RoleAssignments{
		MainCoordinator:               RoleAssignment{Model: "provider-1:terra", SpeedTier: SpeedTierStandard, ReasoningEffort: "high"},
		TaskDecomposition:             RoleAssignment{Model: "provider-1:luna", SpeedTier: SpeedTierStandard, ReasoningEffort: "medium"},
		MainImplementation:            RoleAssignment{Model: "provider-1:terra", SpeedTier: SpeedTierFast, ReasoningEffort: "xhigh"},
		DifficultImplementationReview: RoleAssignment{Model: "provider-1:sol", SpeedTier: SpeedTierFast, ReasoningEffort: "max"},
	}
}

func capableSnapshot() routing.Snapshot {
	return routing.Snapshot{
		Version:          routing.CurrentVersion,
		ActiveProviderID: "provider-1",
		Providers: []routing.Provider{{
			ID: "provider-1", Name: "Codex", ProfileID: "profile-1", Source: "subscription-proxy:codex",
		}},
		ModelRoutes: []routing.ModelRoute{
			trustedPairRoute("provider-1", "terra", "gpt-5.6-terra", SpeedTierStandard),
			trustedPairRoute("provider-1", "terra-fast", "gpt-5.6-terra", SpeedTierFast),
			trustedPairRoute("provider-1", "luna", "gpt-5.6-luna", SpeedTierStandard),
			trustedPairRoute("provider-1", "luna-fast", "gpt-5.6-luna", SpeedTierFast),
			trustedPairRoute("provider-1", "sol", "gpt-5.6-sol", SpeedTierStandard),
			trustedPairRoute("provider-1", "sol-fast", "gpt-5.6-sol", SpeedTierFast),
		},
		ProviderPolicies: map[string]routing.RoutingPolicy{
			"provider-1": {Default: "provider-1:terra"},
		},
		Hydrated: true,
	}
}

func trustedPairRoute(providerID, leaf, physicalID, speedTier string) routing.ModelRoute {
	standardAlias, _ := modelvariants.CodexStandardAlias(physicalID)
	alias := standardAlias
	anchor := providerID + ":" + strings.TrimSuffix(leaf, "-fast")
	if speedTier == SpeedTierFast {
		alias, _ = modelvariants.CodexFastAlias(physicalID)
	}
	return trustedRoute(providerID+":"+leaf, alias, providerID, speedTier, anchor)
}

func trustedRoute(id, alias, providerID, speedTier, anchor string) routing.ModelRoute {
	return routing.ModelRoute{
		ID:                      id,
		Name:                    alias,
		ProviderID:              providerID,
		ProfileModel:            alias,
		SpeedTier:               speedTier,
		StandardAnchor:          anchor,
		Model:                   alias,
		SupportsReasoningEffort: true,
		ReasoningEfforts:        []string{"low", "medium", "high", "xhigh", "max"},
		ReasoningEffortsSource:  "declared",
	}
}

func routeByTestID(t *testing.T, snapshot *routing.Snapshot, id string) *routing.ModelRoute {
	t.Helper()
	for i := range snapshot.ModelRoutes {
		if snapshot.ModelRoutes[i].ID == id {
			return &snapshot.ModelRoutes[i]
		}
	}
	t.Fatalf("route %q not found", id)
	return nil
}

func removeTestRoute(snapshot *routing.Snapshot, id string) {
	out := snapshot.ModelRoutes[:0]
	for _, route := range snapshot.ModelRoutes {
		if route.ID != id {
			out = append(out, route)
		}
	}
	snapshot.ModelRoutes = out
}

func TestFederatedPolicyValidatesExplicitConsentAndArbitraryStandardProvider(t *testing.T) {
	s := capableSnapshot()
	s.Providers = append(s.Providers, routing.Provider{ID: "deepseek", Name: "DeepSeek", ProfileID: "deepseek-profile", Source: "custom"})
	s.ModelRoutes = append(s.ModelRoutes, routing.ModelRoute{ID: "deepseek:reasoner", Name: "deepseek-reasoner", ProviderID: "deepseek", ProfileModel: "deepseek-reasoner", Model: "deepseek-reasoner", SpeedTier: SpeedTierStandard, StandardAnchor: "deepseek:reasoner", SupportsReasoningEffort: true, ReasoningEfforts: []string{"high"}, ReasoningEffortsSource: "declared"})
	s.ProviderPolicies["deepseek"] = routing.RoutingPolicy{Default: "deepseek:reasoner"}
	roles := testRoleAssignments()
	roles.TaskDecomposition = RoleAssignment{ProviderID: "deepseek", Model: "deepseek:reasoner", SpeedTier: SpeedTierStandard, ReasoningEffort: "high", DataScope: DataScopeRepositoryOnly}
	p := NewPolicy("provider-1", roles)
	p.Mode = ModeFederated
	p.FederationConsent = &FederationConsent{Basis: FederationConsentBasisAllWorkflowTiersV1, ProviderIDs: p.CanonicalProviderIDs(), HandoffPolicy: HandoffPolicyBounded, TierHandoffEdges: p.CanonicalTierHandoffEdges(), NeverTransfer: []string{NeverTransferCredentials, NeverTransferSecrets, NeverTransferTranscripts}}
	if _, err := p.ResolveAgainstRouting(s); err != nil {
		t.Fatalf("federated explicit Standard route: %v", err)
	}
	p.FederationConsent = nil
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "consent") {
		t.Fatalf("missing consent error=%v", err)
	}
}

func TestWorkflowPathsDefineExactExecutionAndDataFlows(t *testing.T) {
	want := []WorkflowPath{
		{Tier: TierEconomy, Budget: 1, Roles: []string{RoleMainCoordinator}, DataFlows: []HandoffEdge{}},
		{Tier: TierFocusedEvidence, Budget: 2, Roles: []string{RoleTaskDecomposition, RoleMainCoordinator}, DataFlows: []HandoffEdge{{From: RoleTaskDecomposition, To: RoleMainCoordinator}}},
		{Tier: TierFocusedBuild, Budget: 2, Roles: []string{RoleMainImplementation, RoleMainCoordinator}, DataFlows: []HandoffEdge{{From: RoleMainImplementation, To: RoleMainCoordinator}}},
		{Tier: TierAssurance, Budget: 3, Roles: []string{RoleTaskDecomposition, RoleMainImplementation, RoleMainCoordinator}, DataFlows: []HandoffEdge{{From: RoleTaskDecomposition, To: RoleMainImplementation}, {From: RoleTaskDecomposition, To: RoleMainCoordinator}, {From: RoleMainImplementation, To: RoleMainCoordinator}}},
		{Tier: TierCritical, Budget: 4, Roles: []string{RoleTaskDecomposition, RoleMainImplementation, RoleDifficultImplementationReview, RoleMainCoordinator}, DataFlows: []HandoffEdge{{From: RoleTaskDecomposition, To: RoleMainImplementation}, {From: RoleTaskDecomposition, To: RoleDifficultImplementationReview}, {From: RoleMainImplementation, To: RoleDifficultImplementationReview}, {From: RoleTaskDecomposition, To: RoleMainCoordinator}, {From: RoleMainImplementation, To: RoleMainCoordinator}, {From: RoleDifficultImplementationReview, To: RoleMainCoordinator}}},
	}
	got := WorkflowPaths()
	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Fatalf("WorkflowPaths() = %#v, want %#v", got, want)
	}
	got[4].DataFlows[0] = HandoffEdge{From: "mutated", To: "mutated"}
	if WorkflowPaths()[4].DataFlows[0].From != RoleTaskDecomposition {
		t.Fatal("WorkflowPaths returned mutable canonical data-flow storage")
	}
}

func TestFederationConsentCoversAllExecutablePathsAndIgnoresDefaultTier(t *testing.T) {
	roles := testRoleAssignments()
	roles.TaskDecomposition.ProviderID = "other"
	roles.MainImplementation.ProviderID = "other"
	policy := NewPolicy("provider-1", roles)
	policy.Mode = ModeFederated
	want := policy.CanonicalTierHandoffEdges()
	if len(want) != 5 {
		t.Fatalf("tier edge map count = %d, want 5", len(want))
	}
	for i, path := range WorkflowPaths() {
		if want[i].Tier != path.Tier || path.Budget != []int{1, 2, 2, 3, 4}[i] {
			t.Fatalf("path %d mismatch: workflow=%#v consent=%#v", i, path, want[i])
		}
	}
	if got := want[3].Edges; fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", []HandoffEdge{{From: RoleTaskDecomposition, To: RoleMainCoordinator}, {From: RoleMainImplementation, To: RoleMainCoordinator}}) {
		t.Fatalf("assurance cross-provider edges = %#v", got)
	}
	if got := want[4].Edges; fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", []HandoffEdge{{From: RoleTaskDecomposition, To: RoleDifficultImplementationReview}, {From: RoleMainImplementation, To: RoleDifficultImplementationReview}, {From: RoleTaskDecomposition, To: RoleMainCoordinator}, {From: RoleMainImplementation, To: RoleMainCoordinator}}) {
		t.Fatalf("critical cross-provider edges = %#v", got)
	}
	policy.FederationConsent = &FederationConsent{Basis: FederationConsentBasisAllWorkflowTiersV1, ProviderIDs: policy.CanonicalProviderIDs(), HandoffPolicy: HandoffPolicyBounded, TierHandoffEdges: want, NeverTransfer: []string{NeverTransferCredentials, NeverTransferSecrets, NeverTransferTranscripts}}
	for _, tier := range []string{DefaultTierAdaptive, TierEconomy, TierFocusedEvidence, TierFocusedBuild, TierAssurance, TierCritical} {
		policy.DefaultTier = tier
		if err := policy.Validate(); err != nil {
			t.Fatalf("default tier %q changed all-tier consent validity: %v", tier, err)
		}
	}
}

func TestFederatedPolicyRejectsConsentMismatchOfficialAndFastArbitraryProvider(t *testing.T) {
	roles := testRoleAssignments()
	roles.TaskDecomposition.ProviderID = "other"
	roles.TaskDecomposition.Model = "other:m"
	roles.TaskDecomposition.SpeedTier = SpeedTierStandard
	p := NewPolicy("provider-1", roles)
	p.Mode = ModeFederated
	p.FederationConsent = &FederationConsent{Basis: FederationConsentBasisAllWorkflowTiersV1, ProviderIDs: []string{"other", "provider-1"}, HandoffPolicy: HandoffPolicyBounded, TierHandoffEdges: nil, NeverTransfer: []string{NeverTransferCredentials, NeverTransferSecrets, NeverTransferTranscripts}}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "handoff_edges") {
		t.Fatalf("edge mismatch=%v", err)
	}
	p.FederationConsent.TierHandoffEdges = p.CanonicalTierHandoffEdges()
	p.Roles.TaskDecomposition.ProviderID = routing.OfficialProviderID
	if err := p.Validate(); err == nil || !strings.Contains(strings.ToLower(err.Error()), "official") {
		t.Fatalf("official=%v", err)
	}
}
