package collaboration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrArtifactConflict = errors.New("collaboration artifact conflict")
	ErrArtifactDrift    = errors.New("collaboration artifact drift")
)

type Store struct {
	path string
	mu   sync.Mutex
}

type policyV1RoleModels struct {
	Coordinator string `json:"coordinator"`
	Evidence    string `json:"evidence"`
	Builder     string `json:"builder"`
}

type policyV1 struct {
	Version          int                `json:"version"`
	Enabled          bool               `json:"enabled"`
	ProviderID       string             `json:"provider_id"`
	Models           policyV1RoleModels `json:"models"`
	ReasoningEffort  string             `json:"reasoning_effort"`
	DefaultTier      string             `json:"default_tier"`
	Budgets          TierBudgets        `json:"budgets"`
	MaxParallel      int                `json:"max_parallel"`
	RetryLimit       int                `json:"retry_limit"`
	ArtifactScope    string             `json:"artifact_scope"`
	ManagedArtifacts []ManagedArtifact  `json:"managed_artifacts"`
	UpdatedAt        time.Time          `json:"updated_at"`
}

type policyV2RoleAssignment struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
}

type policyV2RoleAssignments struct {
	MainCoordinator               policyV2RoleAssignment `json:"main_coordinator"`
	TaskDecomposition             policyV2RoleAssignment `json:"task_decomposition"`
	MainImplementation            policyV2RoleAssignment `json:"main_implementation"`
	DifficultImplementationReview policyV2RoleAssignment `json:"difficult_implementation_review"`
}

type policyV2 struct {
	Version          int                     `json:"version"`
	Enabled          bool                    `json:"enabled"`
	ProviderID       string                  `json:"provider_id"`
	Roles            policyV2RoleAssignments `json:"roles"`
	DefaultTier      string                  `json:"default_tier"`
	Budgets          TierBudgets             `json:"budgets"`
	MaxParallel      int                     `json:"max_parallel"`
	RetryLimit       int                     `json:"retry_limit"`
	ArtifactScope    string                  `json:"artifact_scope"`
	ManagedArtifacts []ManagedArtifact       `json:"managed_artifacts"`
	UpdatedAt        time.Time               `json:"updated_at"`
}

type policyV3RoleAssignment struct {
	Model           string `json:"model"`
	SpeedTier       string `json:"speed_tier"`
	ReasoningEffort string `json:"reasoning_effort"`
}
type policyV3RoleAssignments struct {
	MainCoordinator               policyV3RoleAssignment `json:"main_coordinator"`
	TaskDecomposition             policyV3RoleAssignment `json:"task_decomposition"`
	MainImplementation            policyV3RoleAssignment `json:"main_implementation"`
	DifficultImplementationReview policyV3RoleAssignment `json:"difficult_implementation_review"`
}
type policyV3 struct {
	Version          int                     `json:"version"`
	Enabled          bool                    `json:"enabled"`
	ProviderID       string                  `json:"provider_id"`
	Roles            policyV3RoleAssignments `json:"roles"`
	DefaultTier      string                  `json:"default_tier"`
	Budgets          TierBudgets             `json:"budgets"`
	MaxParallel      int                     `json:"max_parallel"`
	RetryLimit       int                     `json:"retry_limit"`
	ArtifactScope    string                  `json:"artifact_scope"`
	ManagedArtifacts []ManagedArtifact       `json:"managed_artifacts"`
	UpdatedAt        time.Time               `json:"updated_at"`
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string {
	return s.path
}

// RestoreBytes restores the policy file during a larger compensated local
// transaction. It does not reinterpret or migrate the supplied snapshot.
func (s *Store) RestoreBytes(content []byte, existed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !existed {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return atomicWriteFile(s.path, content, 0o600)
}

func (s *Store) Snapshot() (Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *Store) Replace(policy Policy) (Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	policy.Version = CurrentVersion
	policy.UpdatedAt = time.Now().UTC()
	policy.ManagedArtifacts = normalizedManifest(policy.ManagedArtifacts)
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return Policy{}, err
	}
	if err := atomicWriteFile(s.path, append(data, '\n'), 0o600); err != nil {
		return Policy{}, err
	}
	return clonePolicy(policy), nil
}

func (s *Store) readLocked() (Policy, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Policy{}, err
	}
	var envelope struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Policy{}, fmt.Errorf("read collaboration policy: %w", err)
	}
	var policy Policy
	switch envelope.Version {
	case 1:
		var legacy policyV1
		if err := decodeStrictPolicy(data, &legacy); err != nil {
			return Policy{}, err
		}
		policy = migratePolicyV1(legacy)
	case 2:
		var legacy policyV2
		if err := decodeStrictPolicy(data, &legacy); err != nil {
			return Policy{}, err
		}
		policy = migratePolicyV2(legacy)
	case 3:
		var legacy policyV3
		if err := decodeStrictPolicy(data, &legacy); err != nil {
			return Policy{}, err
		}
		policy = migratePolicyV3(legacy)
	case CurrentVersion:
		if err := decodeStrictPolicy(data, &policy); err != nil {
			return Policy{}, err
		}
	default:
		return Policy{}, fmt.Errorf("validate collaboration policy: unsupported collaboration policy version %d", envelope.Version)
	}
	policy.ManagedArtifacts = normalizedManifest(policy.ManagedArtifacts)
	if err := policy.Validate(); err != nil {
		return Policy{}, fmt.Errorf("validate collaboration policy: %w", err)
	}
	return clonePolicy(policy), nil
}

func decodeStrictPolicy(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("read collaboration policy: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("read collaboration policy: multiple JSON documents")
		}
		return fmt.Errorf("read collaboration policy: trailing data: %w", err)
	}
	return nil
}

func migratePolicyV1(legacy policyV1) Policy {
	policy := DisabledPolicy()
	policy.Enabled = legacy.Enabled
	policy.ProviderID = strings.TrimSpace(legacy.ProviderID)
	policy.DefaultTier = legacy.DefaultTier
	policy.Budgets = legacy.Budgets
	policy.MaxParallel = legacy.MaxParallel
	policy.RetryLimit = legacy.RetryLimit
	policy.ArtifactScope = legacy.ArtifactScope
	policy.ManagedArtifacts = append([]ManagedArtifact(nil), legacy.ManagedArtifacts...)
	policy.UpdatedAt = legacy.UpdatedAt
	if legacy.Enabled {
		effort := normalizeReasoningEffort(legacy.ReasoningEffort)
		policy.Roles = normalizeRoleAssignments(RoleAssignments{
			MainCoordinator:               RoleAssignment{ProviderID: policy.ProviderID, Model: legacy.Models.Coordinator, SpeedTier: SpeedTierStandard, ReasoningEffort: effort, DataScope: DataScopePriorWork},
			TaskDecomposition:             RoleAssignment{ProviderID: policy.ProviderID, Model: legacy.Models.Evidence, SpeedTier: SpeedTierStandard, ReasoningEffort: effort, DataScope: DataScopeRepositoryOnly},
			MainImplementation:            RoleAssignment{ProviderID: policy.ProviderID, Model: legacy.Models.Coordinator, SpeedTier: SpeedTierStandard, ReasoningEffort: effort, DataScope: DataScopePriorWork},
			DifficultImplementationReview: RoleAssignment{ProviderID: policy.ProviderID, Model: legacy.Models.Builder, SpeedTier: SpeedTierStandard, ReasoningEffort: effort, DataScope: DataScopePriorWork},
		})
	}
	return policy
}

func migratePolicyV2(legacy policyV2) Policy {
	policy := DisabledPolicy()
	policy.Enabled = legacy.Enabled
	policy.ProviderID = strings.TrimSpace(legacy.ProviderID)
	policy.DefaultTier = legacy.DefaultTier
	policy.Budgets = legacy.Budgets
	policy.MaxParallel = legacy.MaxParallel
	policy.RetryLimit = legacy.RetryLimit
	policy.ArtifactScope = legacy.ArtifactScope
	policy.ManagedArtifacts = append([]ManagedArtifact(nil), legacy.ManagedArtifacts...)
	policy.UpdatedAt = legacy.UpdatedAt
	policy.Roles = normalizeRoleAssignments(RoleAssignments{
		MainCoordinator: RoleAssignment{
			ProviderID: policy.ProviderID, DataScope: DataScopePriorWork, Model: legacy.Roles.MainCoordinator.Model, SpeedTier: SpeedTierStandard,
			ReasoningEffort: legacy.Roles.MainCoordinator.ReasoningEffort,
		},
		TaskDecomposition: RoleAssignment{
			ProviderID: policy.ProviderID, DataScope: DataScopeRepositoryOnly, Model: legacy.Roles.TaskDecomposition.Model, SpeedTier: SpeedTierStandard,
			ReasoningEffort: legacy.Roles.TaskDecomposition.ReasoningEffort,
		},
		MainImplementation: RoleAssignment{
			ProviderID: policy.ProviderID, DataScope: DataScopePriorWork, Model: legacy.Roles.MainImplementation.Model, SpeedTier: SpeedTierStandard,
			ReasoningEffort: legacy.Roles.MainImplementation.ReasoningEffort,
		},
		DifficultImplementationReview: RoleAssignment{
			ProviderID: policy.ProviderID, DataScope: DataScopePriorWork, Model: legacy.Roles.DifficultImplementationReview.Model, SpeedTier: SpeedTierStandard,
			ReasoningEffort: legacy.Roles.DifficultImplementationReview.ReasoningEffort,
		},
	})
	return policy
}

func migratePolicyV3(legacy policyV3) Policy {
	p := DisabledPolicy()
	p.Enabled = legacy.Enabled
	p.ProviderID = strings.TrimSpace(legacy.ProviderID)
	p.DefaultTier = legacy.DefaultTier
	p.Budgets = legacy.Budgets
	p.MaxParallel = legacy.MaxParallel
	p.RetryLimit = legacy.RetryLimit
	p.ArtifactScope = legacy.ArtifactScope
	p.ManagedArtifacts = append([]ManagedArtifact(nil), legacy.ManagedArtifacts...)
	p.UpdatedAt = legacy.UpdatedAt
	p.Roles = normalizeRoleAssignments(RoleAssignments{
		MainCoordinator:               RoleAssignment{ProviderID: p.ProviderID, Model: legacy.Roles.MainCoordinator.Model, SpeedTier: legacy.Roles.MainCoordinator.SpeedTier, ReasoningEffort: legacy.Roles.MainCoordinator.ReasoningEffort, DataScope: DataScopePriorWork},
		TaskDecomposition:             RoleAssignment{ProviderID: p.ProviderID, Model: legacy.Roles.TaskDecomposition.Model, SpeedTier: legacy.Roles.TaskDecomposition.SpeedTier, ReasoningEffort: legacy.Roles.TaskDecomposition.ReasoningEffort, DataScope: DataScopeRepositoryOnly},
		MainImplementation:            RoleAssignment{ProviderID: p.ProviderID, Model: legacy.Roles.MainImplementation.Model, SpeedTier: legacy.Roles.MainImplementation.SpeedTier, ReasoningEffort: legacy.Roles.MainImplementation.ReasoningEffort, DataScope: DataScopePriorWork},
		DifficultImplementationReview: RoleAssignment{ProviderID: p.ProviderID, Model: legacy.Roles.DifficultImplementationReview.Model, SpeedTier: legacy.Roles.DifficultImplementationReview.SpeedTier, ReasoningEffort: legacy.Roles.DifficultImplementationReview.ReasoningEffort, DataScope: DataScopePriorWork},
	})
	return p
}

func normalizedManifest(manifest []ManagedArtifact) []ManagedArtifact {
	out := append([]ManagedArtifact(nil), manifest...)
	for i := range out {
		out[i].Path = filepath.Clean(out[i].Path)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

const (
	ArtifactCreate    = "create"
	ArtifactUpdate    = "update"
	ArtifactUnchanged = "unchanged"
)

// ArtifactPlan is both a preview entry and the rollback snapshot used during
// apply. PreviousContent is deliberately excluded from JSON responses.
type ArtifactPlan struct {
	Path             string `json:"path"`
	Action           string `json:"action"`
	PreviousSHA256   string `json:"previous_sha256,omitempty"`
	SHA256           string `json:"sha256"`
	Existed          bool   `json:"existed"`
	Content          []byte `json:"-"`
	PreviousContent  []byte `json:"-"`
	PreviousFileMode os.FileMode
}

// PlanManagedArtifacts is side-effect free. Existing files are writable only
// when their bytes still match an entry in the previous Switch manifest.
func PlanManagedArtifacts(previous []ManagedArtifact, desired []RenderedArtifact) ([]ArtifactPlan, error) {
	owned := make(map[string]string, len(previous))
	for _, artifact := range previous {
		path := filepath.Clean(artifact.Path)
		if _, exists := owned[path]; exists {
			return nil, fmt.Errorf("duplicate managed artifact path %q", path)
		}
		if !validSHA256(artifact.SHA256) {
			return nil, fmt.Errorf("managed artifact %q has invalid SHA-256", path)
		}
		owned[path] = artifact.SHA256
	}

	seen := make(map[string]bool, len(desired))
	plans := make([]ArtifactPlan, 0, len(desired))
	for _, artifact := range desired {
		path := filepath.Clean(artifact.Path)
		if path == "." || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("desired artifact path must be absolute: %q", artifact.Path)
		}
		if seen[path] {
			return nil, fmt.Errorf("duplicate desired artifact path %q", path)
		}
		seen[path] = true
		if artifact.SHA256 != Hash(artifact.Content) {
			return nil, fmt.Errorf("desired artifact %q has a content hash mismatch", path)
		}
		plan := ArtifactPlan{Path: path, Action: ArtifactCreate, SHA256: artifact.SHA256, Content: append([]byte(nil), artifact.Content...)}
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			if _, wasManaged := owned[path]; wasManaged {
				return nil, fmt.Errorf("%w: managed artifact %q is missing; refusing to recreate without review", ErrArtifactDrift, path)
			}
			plans = append(plans, plan)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read collaboration artifact %q: %w", path, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect collaboration artifact %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("collaboration artifact %q is not a regular file", path)
		}
		currentHash := Hash(data)
		expectedHash, wasManaged := owned[path]
		if !wasManaged {
			return nil, fmt.Errorf("%w: unmanaged collaboration artifact collision at %q", ErrArtifactConflict, path)
		}
		if currentHash != expectedHash {
			return nil, fmt.Errorf("%w: managed collaboration artifact at %q no longer matches the Switch manifest", ErrArtifactDrift, path)
		}
		plan.Existed = true
		plan.PreviousSHA256 = currentHash
		plan.PreviousContent = append([]byte(nil), data...)
		plan.PreviousFileMode = info.Mode().Perm()
		if currentHash == artifact.SHA256 {
			plan.Action = ArtifactUnchanged
		} else {
			plan.Action = ArtifactUpdate
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

type WriteFileFunc func(path string, data []byte, mode os.FileMode) error

// ApplyManagedArtifacts writes in deterministic order and compensates every
// earlier write if a later one fails. It never removes pre-existing files and
// rechecks each path immediately before writing so a stale preview cannot
// overwrite a concurrent user edit.
func ApplyManagedArtifacts(plans []ArtifactPlan, writeFile WriteFileFunc) error {
	if writeFile == nil {
		writeFile = atomicWriteFile
	}
	ordered := append([]ArtifactPlan(nil), plans...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	applied := make([]ArtifactPlan, 0, len(ordered))
	for _, plan := range ordered {
		if plan.Action == ArtifactUnchanged {
			if err := verifyArtifactPlanCurrent(plan); err != nil {
				return rollbackAppliedArtifacts(applied, err)
			}
			continue
		}
		if plan.Action != ArtifactCreate && plan.Action != ArtifactUpdate {
			return rollbackAppliedArtifacts(applied, fmt.Errorf("invalid artifact action %q for %q", plan.Action, plan.Path))
		}
		if err := verifyArtifactPlanCurrent(plan); err != nil {
			return rollbackAppliedArtifacts(applied, err)
		}
		if err := writeFile(plan.Path, plan.Content, 0o600); err != nil {
			return rollbackAppliedArtifacts(applied, fmt.Errorf("write collaboration artifact %q: %w", plan.Path, err))
		}
		applied = append(applied, plan)
	}
	return nil
}

func verifyArtifactPlanCurrent(plan ArtifactPlan) error {
	data, err := os.ReadFile(plan.Path)
	if plan.Existed {
		if err != nil {
			return fmt.Errorf("%w: collaboration artifact %q changed after preview", ErrArtifactDrift, plan.Path)
		}
		info, statErr := os.Lstat(plan.Path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || Hash(data) != plan.PreviousSHA256 {
			return fmt.Errorf("%w: collaboration artifact %q changed after preview", ErrArtifactDrift, plan.Path)
		}
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect collaboration artifact %q before write: %w", plan.Path, err)
	}
	return fmt.Errorf("%w: unmanaged collaboration artifact appeared at %q after preview", ErrArtifactConflict, plan.Path)
}

func rollbackAppliedArtifacts(applied []ArtifactPlan, cause error) error {
	if rollbackErr := rollbackArtifacts(applied); rollbackErr != nil {
		return fmt.Errorf("%v; rollback failed: %w", cause, rollbackErr)
	}
	return cause
}

// RestoreManagedArtifacts compensates a previously successful artifact apply.
// It is used when a later config, routing, or policy persistence step fails.
func RestoreManagedArtifacts(plans []ArtifactPlan) error {
	applied := make([]ArtifactPlan, 0, len(plans))
	for _, plan := range plans {
		if plan.Action == ArtifactCreate || plan.Action == ArtifactUpdate {
			applied = append(applied, plan)
		}
	}
	return rollbackArtifacts(applied)
}

func rollbackArtifacts(applied []ArtifactPlan) error {
	var failures []string
	for i := len(applied) - 1; i >= 0; i-- {
		plan := applied[i]
		data, err := os.ReadFile(plan.Path)
		if err != nil {
			if os.IsNotExist(err) && !plan.Existed {
				continue
			}
			failures = append(failures, fmt.Sprintf("rollback incomplete for %q: current artifact cannot be verified: %v", plan.Path, err))
			continue
		}
		info, statErr := os.Lstat(plan.Path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || Hash(data) != plan.SHA256 {
			failures = append(failures, fmt.Sprintf("rollback incomplete for %q: current artifact no longer matches transaction desired hash", plan.Path))
			continue
		}
		if plan.Existed {
			mode := plan.PreviousFileMode
			if mode == 0 {
				mode = 0o600
			}
			if err := atomicWriteFile(plan.Path, plan.PreviousContent, mode); err != nil {
				failures = append(failures, fmt.Sprintf("restore %q: %v", plan.Path, err))
			}
			continue
		}
		if err := os.Remove(plan.Path); err != nil && !os.IsNotExist(err) {
			failures = append(failures, fmt.Sprintf("remove newly created %q: %v", plan.Path, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular file %q", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(parent, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		if runtime.GOOS == "windows" {
			if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
				return err
			}
			if err := os.Rename(tmpName, path); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return nil
}
