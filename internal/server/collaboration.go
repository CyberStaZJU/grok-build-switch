package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"grok_switch/internal/collaboration"
	grokconfig "grok_switch/internal/config"
	"grok_switch/internal/routing"
)

type collaborationRequest struct {
	Version           int                              `json:"version"`
	Enabled           *bool                            `json:"enabled"`
	Mode              string                           `json:"mode"`
	ProviderID        string                           `json:"provider_id"`
	FederationConsent *collaboration.FederationConsent `json:"federation_consent,omitempty"`
	Roles             collaboration.RoleAssignments    `json:"roles"`
	DefaultTier       string                           `json:"default_tier"`
	Confirmed         bool                             `json:"confirmed,omitempty"`
	Fingerprint       string                           `json:"fingerprint,omitempty"`
}

type collaborationArtifactDTO struct {
	Path             string `json:"path"`
	Action           string `json:"action"`
	PreviousSHA256   string `json:"previous_sha256,omitempty"`
	SHA256           string `json:"sha256"`
	Content          string `json:"content"`
	PreviousContent  string `json:"previous_content,omitempty"`
	PreviouslyExists bool   `json:"previously_exists"`
}

type collaborationPreviewDTO struct {
	Policy         collaboration.Policy       `json:"policy"`
	RoutingBefore  routing.RoutingPolicy      `json:"routing_before"`
	RoutingAfter   routing.RoutingPolicy      `json:"routing_after"`
	RoutingChanged bool                       `json:"routing_changed"`
	ConfigBefore   string                     `json:"config_before"`
	ConfigAfter    string                     `json:"config_after"`
	ConfigChanged  bool                       `json:"config_changed"`
	Artifacts      []collaborationArtifactDTO `json:"artifacts"`
	Fingerprint    string                     `json:"fingerprint"`
	Warnings       []string                   `json:"warnings"`
}

type collaborationStatusDTO struct {
	Configured bool                  `json:"configured"`
	Valid      bool                  `json:"valid"`
	Drifted    bool                  `json:"drifted"`
	Policy     *collaboration.Policy `json:"policy,omitempty"`
	Issues     []string              `json:"issues"`
}

type collaborationSpecDTO struct {
	SchemaVersion              int                          `json:"schema_version"`
	CollaborationPolicyVersion int                          `json:"collaboration_policy_version"`
	WorkflowPaths              []collaboration.WorkflowPath `json:"workflow_paths"`
}

const collaborationSpecSchemaVersion = 1

const federatedStructuralBlocker = "federated mode is structurally blocked: current Grok config activation serializes only the active provider, so non-active provider routes cannot be referenced safely without credential/config merging"

type collaborationPrepared struct {
	preview        collaborationPreviewDTO
	policy         collaboration.Policy
	nextRouting    routing.Snapshot
	artifactPlans  []collaboration.ArtifactPlan
	oldConfig      []byte
	configExisted  bool
	oldRouting     []byte
	routingExisted bool
	oldPolicy      []byte
	policyExisted  bool
}

func (s *Server) handleCollaborationSpec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if !isLoopbackRequest(r) {
		writeError(w, fmt.Errorf("collaboration 仅允许本机访问"), http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, collaborationSpecDTO{
		SchemaVersion:              collaborationSpecSchemaVersion,
		CollaborationPolicyVersion: collaboration.CurrentVersion,
		WorkflowPaths:              collaboration.WorkflowPaths(),
	})
}

func (s *Server) handleCollaboration(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeError(w, fmt.Errorf("collaboration 仅允许本机访问"), http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleCollaborationStatus(w)
	case http.MethodPut:
		s.handleCollaborationApply(w, r)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleCollaborationPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !isLoopbackRequest(r) {
		writeError(w, fmt.Errorf("collaboration 仅允许本机访问"), http.StatusForbidden)
		return
	}
	if err := s.collaborationReady(); err != nil {
		writeError(w, err, http.StatusServiceUnavailable)
		return
	}
	var request collaborationRequest
	if err := decodeManagementJSON(w, r, &request); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if request.Confirmed || strings.TrimSpace(request.Fingerprint) != "" {
		writeError(w, fmt.Errorf("preview 请求不能包含 confirmed 或 fingerprint"), http.StatusBadRequest)
		return
	}

	s.collaborationMu.Lock()
	defer s.collaborationMu.Unlock()
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	prepared, err := s.prepareCollaborationLocked(request)
	if err != nil {
		writeCollaborationPreparationError(w, err)
		return
	}
	writeJSON(w, prepared.preview)
}

func (s *Server) handleCollaborationApply(w http.ResponseWriter, r *http.Request) {
	if err := s.collaborationReady(); err != nil {
		writeError(w, err, http.StatusServiceUnavailable)
		return
	}
	var request collaborationRequest
	if err := decodeManagementJSON(w, r, &request); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if !request.Confirmed || strings.TrimSpace(request.Fingerprint) == "" {
		writeError(w, fmt.Errorf("应用 collaboration 前必须确认最新 preview 并提交 fingerprint"), http.StatusBadRequest)
		return
	}

	s.collaborationMu.Lock()
	defer s.collaborationMu.Unlock()
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	prepared, err := s.prepareCollaborationLocked(request)
	if err != nil {
		writeCollaborationPreparationError(w, err)
		return
	}
	if request.Fingerprint != prepared.preview.Fingerprint {
		writeError(w, fmt.Errorf("collaboration preview 已过期，请重新预览后确认"), http.StatusBadRequest)
		return
	}
	persistedPolicy, err := s.applyCollaborationLocked(prepared)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	s.changed()
	status := collaborationStatusDTO{Configured: true, Valid: true, Policy: &persistedPolicy, Issues: []string{}}
	if artifactIssues, drifted := s.collaborationArtifactStatus(persistedPolicy); len(artifactIssues) > 0 {
		status.Valid = false
		status.Drifted = drifted
		status.Issues = append(status.Issues, artifactIssues...)
	}
	writeJSON(w, map[string]any{"status": status, "preview": prepared.preview})
}

func (s *Server) handleCollaborationStatus(w http.ResponseWriter) {
	if err := s.collaborationReady(); err != nil {
		writeError(w, err, http.StatusServiceUnavailable)
		return
	}
	s.collaborationMu.Lock()
	defer s.collaborationMu.Unlock()
	s.routingMu.Lock()
	defer s.routingMu.Unlock()

	policy, err := s.Collaboration.Snapshot()
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, collaborationStatusDTO{Configured: false, Valid: false, Issues: []string{}})
		return
	}
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	status := collaborationStatusDTO{Configured: true, Valid: true, Policy: &policy, Issues: []string{}}
	if artifactIssues, drifted := s.collaborationArtifactStatus(policy); len(artifactIssues) > 0 {
		status.Valid = false
		status.Drifted = drifted
		status.Issues = append(status.Issues, artifactIssues...)
	}
	if !policy.Enabled {
		writeJSON(w, status)
		return
	}
	if policy.Mode == collaboration.ModeFederated {
		status.Valid = false
		status.Issues = append(status.Issues, federatedStructuralBlocker)
		writeJSON(w, status)
		return
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		status.Valid = false
		status.Issues = append(status.Issues, fmt.Sprintf("读取当前路由失败: %v", err))
		writeJSON(w, status)
		return
	}
	profilesList, err := s.Profiles.List()
	if err != nil {
		status.Valid = false
		status.Issues = append(status.Issues, fmt.Sprintf("读取 Profile 失败: %v", err))
		writeJSON(w, status)
		return
	}
	hydrated, err := routing.ProjectWithSnapshot(profilesList, stored)
	if err != nil {
		status.Valid = false
		status.Issues = append(status.Issues, fmt.Sprintf("重建路由失败: %v", err))
	} else if err := policy.ValidateAgainstRouting(hydrated); err != nil {
		status.Valid = false
		status.Issues = append(status.Issues, err.Error())
	}
	if matches, matchErr := grokconfig.CurrentMatchesRoutingStrictDefaults(s.Switcher.ConfigPath, hydrated); matchErr != nil || !matches {
		status.Valid = false
		if matchErr != nil {
			status.Issues = append(status.Issues, fmt.Sprintf("检查 config.toml 失败: %v", matchErr))
		} else {
			status.Issues = append(status.Issues, "config.toml 与 collaboration 路由不一致")
		}
	}
	writeJSON(w, status)
}

// collaborationArtifactStatus validates both the manifest boundary and the
// current file objects. The preset owns exactly five canonical Grok Home paths;
// accepting an empty, partial, relocated, symlinked, or non-regular manifest
// would make a configured policy appear healthy without proving its artifacts.
// Disabled policies retain ownership, so their files remain observable for
// drift even though routing/config checks are intentionally skipped.
func (s *Server) collaborationArtifactStatus(policy collaboration.Policy) ([]string, bool) {
	// A never-enabled disabled policy may legitimately have no ownership yet.
	// Once a policy is enabled or retains role/provider state or a manifest, the
	// complete five-file boundary is mandatory and remains observable.
	requiresManifest := policy.Enabled || strings.TrimSpace(policy.ProviderID) != "" || policy.Roles != (collaboration.RoleAssignments{}) || len(policy.ManagedArtifacts) > 0
	if !requiresManifest {
		return nil, false
	}

	expectedPaths := collaboration.ArtifactPathsForGrokHome(s.Paths.GrokHome)
	canonical := expectedPaths.CanonicalPaths()

	issues := make([]string, 0)
	if err := collaboration.ValidateCanonicalManifest(policy.ManagedArtifacts, expectedPaths); err != nil {
		issues = append(issues, fmt.Sprintf("受管文件 manifest 非 canonical: %v", err))
	}
	actual := make(map[string]collaboration.ManagedArtifact, len(policy.ManagedArtifacts))
	for _, artifact := range policy.ManagedArtifacts {
		actual[filepath.Clean(artifact.Path)] = artifact
	}

	drifted := len(issues) > 0
	for _, path := range canonical {
		path = filepath.Clean(path)
		artifact, ok := actual[path]
		if !ok {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			drifted = true
			issues = append(issues, fmt.Sprintf("受管文件不可检查 %s: %v", path, err))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			drifted = true
			issues = append(issues, fmt.Sprintf("受管文件不是普通文件: %s", path))
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			drifted = true
			issues = append(issues, fmt.Sprintf("受管文件不可读 %s: %v", path, err))
			continue
		}
		if collaboration.Hash(data) != artifact.SHA256 {
			drifted = true
			issues = append(issues, fmt.Sprintf("受管文件已变更: %s", path))
		}
	}
	return issues, drifted
}

func (s *Server) collaborationReady() error {
	if s.Collaboration == nil || s.Routing == nil || s.Profiles == nil || s.Switcher == nil {
		return fmt.Errorf("collaboration 服务未初始化")
	}
	if strings.TrimSpace(s.Paths.GrokHome) == "" {
		return fmt.Errorf("Grok Home 未配置")
	}
	return nil
}

func (s *Server) prepareCollaborationLocked(request collaborationRequest) (collaborationPrepared, error) {
	previousPolicy, previousPolicyErr := s.Collaboration.Snapshot()
	if previousPolicyErr != nil && !errors.Is(previousPolicyErr, os.ErrNotExist) {
		return collaborationPrepared{}, previousPolicyErr
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	var policy collaboration.Policy
	if enabled {
		if request.Version != collaboration.CurrentVersion {
			return collaborationPrepared{}, fmt.Errorf("collaboration request version must be %d", collaboration.CurrentVersion)
		}
		policy = collaboration.NewPolicy(request.ProviderID, request.Roles)
		policy.Mode = request.Mode
		policy.FederationConsent = request.FederationConsent
		// v4 requests must carry every role provider and the workflow-derived canonical data scope explicitly; constructor defaults are only for internal compatibility.
		for name, assignment := range map[string]collaboration.RoleAssignment{"main coordinator": request.Roles.MainCoordinator, "task decomposition": request.Roles.TaskDecomposition, "main implementation": request.Roles.MainImplementation, "difficult implementation/review": request.Roles.DifficultImplementationReview} {
			if strings.TrimSpace(assignment.ProviderID) == "" || strings.TrimSpace(assignment.DataScope) == "" {
				return collaborationPrepared{}, fmt.Errorf("%s provider_id and data_scope are required in v4 requests", name)
			}
		}
		if strings.TrimSpace(request.DefaultTier) != "" {
			policy.DefaultTier = strings.TrimSpace(request.DefaultTier)
		}
	} else {
		policy = collaboration.DisabledPolicy()
		if request.Version != collaboration.CurrentVersion || request.Mode != "" || strings.TrimSpace(request.ProviderID) != "" || request.FederationConsent != nil || request.Roles != (collaboration.RoleAssignments{}) || strings.TrimSpace(request.DefaultTier) != "" {
			return collaborationPrepared{}, fmt.Errorf("disable 请求只能包含 version=4 与 enabled=false")
		}
	}
	if previousPolicyErr == nil {
		policy.ManagedArtifacts = append([]collaboration.ManagedArtifact(nil), previousPolicy.ManagedArtifacts...)
		requiresCanonicalManifest := previousPolicy.Enabled || strings.TrimSpace(previousPolicy.ProviderID) != "" || previousPolicy.Roles != (collaboration.RoleAssignments{}) || len(previousPolicy.ManagedArtifacts) > 0
		if requiresCanonicalManifest {
			artifactPaths := collaboration.ArtifactPathsForGrokHome(s.Paths.GrokHome)
			if err := collaboration.ValidateCanonicalManifest(previousPolicy.ManagedArtifacts, artifactPaths); err != nil {
				return collaborationPrepared{}, fmt.Errorf("previous collaboration manifest is not canonical: %w", err)
			}
		}
	}

	if !policy.Enabled {
		if previousPolicyErr == nil {
			// Disable is policy-only, but the Collaboration store still owns its
			// provider metadata and four role assignments. Preserve them so a later
			// status read or re-enable UI can describe the disabled configuration;
			// only the enabled flag changes.
			policy.Mode = previousPolicy.Mode
			policy.ProviderID = previousPolicy.ProviderID
			policy.FederationConsent = previousPolicy.FederationConsent
			policy.Roles = previousPolicy.Roles
			policy.DefaultTier = previousPolicy.DefaultTier
		}
		if err := policy.Validate(); err != nil {
			return collaborationPrepared{}, err
		}
		oldConfig, configExisted, err := readOptionalFile(s.Switcher.ConfigPath)
		if err != nil {
			return collaborationPrepared{}, err
		}
		oldPolicy, policyExisted, err := readOptionalFile(s.Collaboration.Path())
		if err != nil {
			return collaborationPrepared{}, err
		}
		preview := collaborationPreviewDTO{
			Policy:       policy,
			ConfigBefore: string(oldConfig),
			ConfigAfter:  string(oldConfig),
			Artifacts:    []collaborationArtifactDTO{},
			Warnings: []string{
				"停用只切换 collaboration policy 的 enabled 状态；会保留 provider、四角色选择、默认 tier 与 manifest。",
				"停用不会删除或改写受管 role/workflow，也不会修改当前 config.toml 或 routing.json。",
				"Switch 不启动 agent，也不保存消息或 transcript。",
			},
		}
		preview.Fingerprint = collaborationPreviewFingerprint(preview)
		return collaborationPrepared{
			preview: preview, policy: policy,
			oldConfig: oldConfig, configExisted: configExisted,
			oldPolicy: oldPolicy, policyExisted: policyExisted,
		}, nil
	}

	stored, err := s.Routing.Snapshot()
	if err != nil {
		return collaborationPrepared{}, err
	}
	profilesList, err := s.Profiles.List()
	if err != nil {
		return collaborationPrepared{}, err
	}
	hydrated, err := routing.ProjectWithSnapshot(profilesList, stored)
	if err != nil {
		return collaborationPrepared{}, err
	}
	resolvedRoles, err := policy.ResolveAgainstRouting(hydrated)
	if err != nil {
		return collaborationPrepared{}, err
	}
	if policy.Mode == collaboration.ModeFederated {
		return collaborationPrepared{}, fmt.Errorf("%s", federatedStructuralBlocker)
	}

	artifactPaths := collaboration.ArtifactPathsForGrokHome(s.Paths.GrokHome)
	artifacts := []collaboration.RenderedArtifact{}
	if policy.Enabled {
		artifacts, err = collaboration.RenderArtifacts(policy, hydrated, artifactPaths)
		if err != nil {
			return collaborationPrepared{}, err
		}
	}
	plans, err := collaboration.PlanManagedArtifacts(policy.ManagedArtifacts, artifacts)
	if err != nil {
		return collaborationPrepared{}, err
	}
	if policy.Enabled {
		policy.ManagedArtifacts = collaboration.ManifestForArtifacts(artifacts)
	}
	if err := policy.Validate(); err != nil {
		return collaborationPrepared{}, err
	}

	nextState := stored
	if policy.Enabled {
		nextState.Policy = routing.RoutingPolicy{}
		nextState.ActiveProviderID = policy.ProviderID
		if nextState.ProviderPolicies == nil {
			nextState.ProviderPolicies = map[string]routing.RoutingPolicy{}
		}
		currentProviderPolicy := nextState.ProviderPolicies[policy.ProviderID]
		currentProviderPolicy.Default = resolvedRoles.MainCoordinator.Route.ID
		currentProviderPolicy.DefaultReasoningEffort = resolvedRoles.MainCoordinator.ReasoningEffort
		// Collaboration policy schema v4 owns its four user-level role files. Generic
		// explore/plan routing remains under the ordinary routing controls and is
		// preserved rather than overloaded with unrelated semantic roles.
		nextState.ProviderPolicies[policy.ProviderID] = currentProviderPolicy
	}
	nextRouting, err := routing.ProjectWithSnapshot(profilesList, nextState)
	if err != nil {
		return collaborationPrepared{}, err
	}
	if err := validateActiveWebSearch(nextRouting); err != nil {
		return collaborationPrepared{}, err
	}
	if err := validateRoutingReasoningEffort(nextRouting); err != nil {
		return collaborationPrepared{}, err
	}

	oldConfig, configExisted, err := readOptionalFile(s.Switcher.ConfigPath)
	if err != nil {
		return collaborationPrepared{}, err
	}
	oldRouting, routingExisted, err := readOptionalFile(s.Routing.Path())
	if err != nil {
		return collaborationPrepared{}, err
	}
	oldPolicy, policyExisted, err := readOptionalFile(s.Collaboration.Path())
	if err != nil {
		return collaborationPrepared{}, err
	}
	configAfter := append([]byte(nil), oldConfig...)
	if policy.Enabled {
		configAfter, err = grokconfig.PreviewRouting(s.Switcher.ConfigPath, nextRouting)
		if err != nil {
			return collaborationPrepared{}, err
		}
	}
	artifactDTOs := make([]collaborationArtifactDTO, 0, len(plans))
	for _, plan := range plans {
		artifactDTOs = append(artifactDTOs, collaborationArtifactDTO{
			Path: plan.Path, Action: plan.Action, PreviousSHA256: plan.PreviousSHA256,
			SHA256: plan.SHA256, Content: string(plan.Content), PreviousContent: string(plan.PreviousContent), PreviouslyExists: plan.Existed,
		})
	}
	beforePolicy := hydrated.ActivePolicy()
	afterPolicy := nextRouting.ActivePolicy()
	preview := collaborationPreviewDTO{
		Policy: policy, RoutingBefore: beforePolicy, RoutingAfter: afterPolicy,
		RoutingChanged: !durablePolicyEqual(beforePolicy, afterPolicy) || hydrated.ActiveProviderID != nextRouting.ActiveProviderID,
		ConfigBefore:   string(oldConfig), ConfigAfter: string(configAfter), ConfigChanged: string(oldConfig) != string(configAfter),
		Artifacts: artifactDTOs,
		Warnings: []string{
			"四个角色分别保存 Standard 模型锚点，并独立选择速度档与推理强度；多个角色可以复用同一模型。",
			"Standard 不注入 priority；Fast 只解析到 exact-registry 可信别名，缺失或不可信时直接失败，不会静默回退。",
			"只有 resolved Standard/Fast 路由由 declared 或 probe 明确支持所选 effort 时才能应用；速度档与推理强度彼此独立。",
			"生成 workflow 严格串行并拒绝默认 128 预算；必须由 workflow tool 传入 tier 对应的 1/2/3/4。",
			"Collaboration 不接管普通 explore/plan 路由；Switch 只生成配置，不启动 agent，也不保存消息或 transcript。",
		},
	}
	if policy.UsesFast() {
		preview.Warnings = append(preview.Warnings, "Fast 会请求上游 priority service tier，通常更快但会消耗更多订阅 credits；仅在确实需要低延迟的角色上启用。")
	}
	preview.Fingerprint = collaborationPreviewFingerprint(preview)
	return collaborationPrepared{
		preview: preview, policy: policy, nextRouting: nextRouting, artifactPlans: plans,
		oldConfig: oldConfig, configExisted: configExisted,
		oldRouting: oldRouting, routingExisted: routingExisted,
		oldPolicy: oldPolicy, policyExisted: policyExisted,
	}, nil
}

func (s *Server) applyCollaborationLocked(prepared collaborationPrepared) (collaboration.Policy, error) {
	persistPolicy := s.persistCollaboration
	if persistPolicy == nil {
		persistPolicy = s.Collaboration.Replace
	}
	if !prepared.policy.Enabled {
		persisted, err := persistPolicy(prepared.policy)
		if err != nil {
			return collaboration.Policy{}, fmt.Errorf("保存 collaboration policy 失败: %w", err)
		}
		return persisted, nil
	}
	if err := collaboration.ApplyManagedArtifacts(prepared.artifactPlans, nil); err != nil {
		return collaboration.Policy{}, err
	}
	if err := s.Switcher.ApplyRouting(prepared.nextRouting); err != nil {
		if rollbackErr := collaboration.RestoreManagedArtifacts(prepared.artifactPlans); rollbackErr != nil {
			return collaboration.Policy{}, fmt.Errorf("应用 collaboration 路由失败: %v；恢复受管文件失败: %w", err, rollbackErr)
		}
		return collaboration.Policy{}, err
	}
	if _, err := s.Routing.Replace(prepared.nextRouting); err != nil {
		return collaboration.Policy{}, s.rollbackCollaborationAfterRoutingFailure(prepared, fmt.Errorf("保存 collaboration 路由失败: %w", err))
	}
	persisted, err := persistPolicy(prepared.policy)
	if err != nil {
		return collaboration.Policy{}, s.rollbackCollaborationAfterRoutingFailure(prepared, fmt.Errorf("保存 collaboration policy 失败: %w", err))
	}
	return persisted, nil
}

func (s *Server) rollbackCollaborationAfterRoutingFailure(prepared collaborationPrepared, cause error) error {
	var failures []string
	if err := s.Collaboration.RestoreBytes(prepared.oldPolicy, prepared.policyExisted); err != nil {
		failures = append(failures, "policy: "+err.Error())
	}
	if err := s.Routing.RestoreBytes(prepared.oldRouting, prepared.routingExisted); err != nil {
		failures = append(failures, "routing: "+err.Error())
	}
	if err := s.Switcher.RestoreConfigState(prepared.oldConfig, prepared.configExisted); err != nil {
		failures = append(failures, "config: "+err.Error())
	}
	if err := collaboration.RestoreManagedArtifacts(prepared.artifactPlans); err != nil {
		failures = append(failures, "artifacts: "+err.Error())
	}
	if len(failures) > 0 {
		return fmt.Errorf("%v；回滚失败: %s", cause, strings.Join(failures, "; "))
	}
	return cause
}

func writeCollaborationPreparationError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, collaboration.ErrArtifactConflict) || errors.Is(err, collaboration.ErrArtifactDrift) {
		status = http.StatusConflict
	}
	writeError(w, err, status)
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func collaborationPreviewFingerprint(preview collaborationPreviewDTO) string {
	type fingerprintArtifact struct {
		Path           string `json:"path"`
		Action         string `json:"action"`
		PreviousSHA256 string `json:"previous_sha256,omitempty"`
		SHA256         string `json:"sha256"`
	}
	artifacts := make([]fingerprintArtifact, 0, len(preview.Artifacts))
	for _, artifact := range preview.Artifacts {
		artifacts = append(artifacts, fingerprintArtifact{
			Path: artifact.Path, Action: artifact.Action,
			PreviousSHA256: artifact.PreviousSHA256, SHA256: artifact.SHA256,
		})
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	payload := struct {
		Policy       collaboration.Policy  `json:"policy"`
		RoutingAfter routing.RoutingPolicy `json:"routing_after"`
		ConfigAfter  string                `json:"config_after"`
		Artifacts    []fingerprintArtifact `json:"artifacts"`
	}{Policy: preview.Policy, RoutingAfter: preview.RoutingAfter, ConfigAfter: preview.ConfigAfter, Artifacts: artifacts}
	payload.Policy.UpdatedAt = payload.Policy.UpdatedAt.UTC()
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
