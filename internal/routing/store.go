package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"grok_switch/internal/profiles"
	"grok_switch/internal/recovery"
)

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Path() string {
	return s.path
}

// Initialize creates routing.json from the legacy profile store exactly once.
// Existing routing state is returned unchanged, making startup migration idempotent.
func (s *Store) Initialize(profileStore *profiles.Store) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, exists, dirty, err := s.readLocked()
	if err != nil {
		return Snapshot{}, err
	}
	if exists {
		if dirty {
			if err := s.writeLocked(snapshot); err != nil {
				return Snapshot{}, err
			}
		}
		return cloneSnapshot(snapshot), nil
	}
	legacy, err := profileStore.List()
	if err != nil {
		return Snapshot{}, fmt.Errorf("project profiles into routing: %w", err)
	}
	snapshot = Project(legacy)
	// One-time migration: old profiles.json files may carry web_search_model
	// and subagents_models fields that are now owned by the routing policy.
	// If the active profile has these legacy values and the freshly-projected
	// policy does not yet set them, carry them forward.
	if migrated := applyLegacyProfileRouting(profileStore, &snapshot); migrated {
		if err := snapshot.Validate(); err != nil {
			return Snapshot{}, err
		}
	}
	if err := s.writeLocked(snapshot); err != nil {
		return Snapshot{}, err
	}
	return cloneSnapshot(snapshot), nil
}

func (s *Store) Snapshot() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, exists, dirty, err := s.readLocked()
	if err != nil {
		return Snapshot{}, err
	}
	if !exists {
		return Snapshot{}, os.ErrNotExist
	}
	if dirty {
		if err := s.writeLocked(snapshot); err != nil {
			return Snapshot{}, err
		}
	}
	return cloneSnapshot(snapshot), nil
}

func (s *Store) UpdatePolicy(policy RoutingPolicy) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, exists, _, err := s.readLocked()
	if err != nil {
		return Snapshot{}, err
	}
	if !exists {
		return Snapshot{}, os.ErrNotExist
	}
	providerID := snapshot.ActiveProviderID
	if policy.Official {
		providerID = OfficialProviderID
		policy.Official = false
	}
	if providerID == "" {
		for _, ref := range []string{policy.Default, policy.WebSearch, policy.Subagents.Explore, policy.Subagents.Plan} {
			if route, ok := snapshot.Route(ref); ok {
				providerID = route.ProviderID
				break
			}
		}
	}
	if snapshot.ProviderPolicies == nil {
		snapshot.ProviderPolicies = map[string]RoutingPolicy{}
	}
	snapshot.Policy = RoutingPolicy{}
	snapshot.ActiveProviderID = providerID
	snapshot.ProviderPolicies[providerID] = policy
	return s.replaceLocked(snapshot)
}

func (s *Store) UpdateActiveProvider(providerID string, policy RoutingPolicy) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot, exists, _, err := s.readLocked()
	if err != nil {
		return Snapshot{}, err
	}
	if !exists {
		return Snapshot{}, os.ErrNotExist
	}
	if snapshot.ProviderPolicies == nil {
		snapshot.ProviderPolicies = map[string]RoutingPolicy{}
	}
	snapshot.Policy = RoutingPolicy{}
	snapshot.ActiveProviderID = providerID
	snapshot.ProviderPolicies[providerID] = policy
	return s.replaceLocked(snapshot)
}

// Replace stores a freshly projected catalog and policy. Runtime credentials,
// endpoints, and headers are stripped by writeLocked before persistence.
func (s *Store) Replace(snapshot Snapshot) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaceLocked(snapshot)
}

func (s *Store) replaceLocked(snapshot Snapshot) (Snapshot, error) {
	snapshot.Version = CurrentVersion
	snapshot.UpdatedAt = time.Now().UTC()
	if !policyEmpty(snapshot.Policy) || snapshot.Policy.Official {
		providerID := snapshot.ActiveProviderID
		if snapshot.Policy.Official {
			providerID = OfficialProviderID
			snapshot.Policy.Official = false
		}
		if snapshot.ProviderPolicies == nil {
			snapshot.ProviderPolicies = map[string]RoutingPolicy{}
		}
		snapshot.ActiveProviderID = providerID
		snapshot.ProviderPolicies[providerID] = snapshot.Policy
	}
	snapshot.Policy = snapshot.ProviderPolicies[snapshot.ActiveProviderID]
	snapshot.Policy.Official = snapshot.IsOfficial()
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	if err := s.writeLocked(snapshot); err != nil {
		return Snapshot{}, err
	}
	out := cloneSnapshot(sanitizedSnapshot(snapshot))
	out.Policy = policyWithRouteNames(out, out.ProviderPolicies[out.ActiveProviderID])
	out.Policy.Official = out.ActiveProviderID == OfficialProviderID
	return out, nil
}

func (s *Store) readLocked() (Snapshot, bool, bool, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return Snapshot{}, false, false, err
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, false, false, nil
	}
	if err != nil {
		return Snapshot{}, false, false, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		cause := fmt.Errorf("read routing: %w", err)
		backup, backupErr := recovery.BackupCorrupt(s.path)
		if backupErr != nil {
			return Snapshot{}, false, false, fmt.Errorf("%v; backup corrupt routing: %w", cause, backupErr)
		}
		return Snapshot{}, false, false, fmt.Errorf("%v; corrupt file moved to %s", cause, backup)
	}
	dirty := containsLegacyCredentials(data)
	if snapshot.Version < CurrentVersion {
		var legacy struct {
			Version     int           `json:"version"`
			Providers   []Provider    `json:"providers"`
			ModelRoutes []ModelRoute  `json:"model_routes"`
			Policy      RoutingPolicy `json:"policy"`
			UpdatedAt   time.Time     `json:"updated_at"`
		}
		if err := json.Unmarshal(data, &legacy); err != nil {
			return Snapshot{}, false, false, err
		}
		snapshot = migrateV1(legacy.Providers, legacy.ModelRoutes, legacy.Policy, legacy.UpdatedAt)
		dirty = true
	}
	for i := range snapshot.ModelRoutes {
		if snapshot.ModelRoutes[i].ProfileModel != "" {
			continue
		}
		if _, modelName, ok := strings.Cut(snapshot.ModelRoutes[i].ID, ":"); ok && modelName != "" {
			snapshot.ModelRoutes[i].ProfileModel = modelName
		} else {
			snapshot.ModelRoutes[i].ProfileModel = snapshot.ModelRoutes[i].Name
		}
		dirty = true
	}
	if snapshot.ProviderPolicies == nil {
		snapshot.ProviderPolicies = map[string]RoutingPolicy{}
		dirty = true
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, false, false, fmt.Errorf("validate routing: %w", err)
	}
	snapshot.Policy = policyWithRouteNames(snapshot, snapshot.ProviderPolicies[snapshot.ActiveProviderID])
	snapshot.Policy.Official = snapshot.IsOfficial()
	return snapshot, true, dirty, nil
}

func containsLegacyCredentials(data []byte) bool {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	return containsSensitiveJSONKey(raw)
}

func containsSensitiveJSONKey(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "api_key", "extra_headers":
				return true
			}
			if containsSensitiveJSONKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsSensitiveJSONKey(child) {
				return true
			}
		}
	}
	return false
}

func (s *Store) writeLocked(snapshot Snapshot) error {
	if snapshot.Version == 0 {
		snapshot.Version = CurrentVersion
	}
	snapshot = sanitizedSnapshot(snapshot)
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.path, append(data, '\n'))
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
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
	return os.Chmod(path, 0o600)
}

func sanitizedSnapshot(snapshot Snapshot) Snapshot {
	out := cloneSnapshot(snapshot)
	out.Policy = out.ProviderPolicies[out.ActiveProviderID]
	out.Policy.Official = out.ActiveProviderID == OfficialProviderID
	out.Hydrated = false
	for i := range out.Providers {
		out.Providers[i].UpstreamFormat = ""
		out.Providers[i].BaseURL = ""
		out.Providers[i].APIKey = ""
	}
	for i := range out.ModelRoutes {
		out.ModelRoutes[i].Model = ""
		out.ModelRoutes[i].APIBackend = ""
		out.ModelRoutes[i].BaseURL = ""
		out.ModelRoutes[i].APIKey = ""
		out.ModelRoutes[i].ExtraHeaders = nil
	}
	return out
}

func migrateV1(providers []Provider, routes []ModelRoute, legacy RoutingPolicy, updatedAt time.Time) Snapshot {
	snapshot := Snapshot{Version: CurrentVersion, Providers: providers, ModelRoutes: routes, ProviderPolicies: map[string]RoutingPolicy{}, UpdatedAt: updatedAt}
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = time.Now().UTC()
	}
	for i := range snapshot.ModelRoutes {
		if snapshot.ModelRoutes[i].ProfileModel == "" {
			_, snapshot.ModelRoutes[i].ProfileModel, _ = strings.Cut(snapshot.ModelRoutes[i].ID, ":")
		}
	}
	active := ""
	if route, ok := snapshot.Route(legacy.Default); ok {
		active = route.ProviderID
	}
	if legacy.Official {
		active = OfficialProviderID
	}
	if active == "" && len(providers) > 0 {
		active = providers[0].ID
	}
	for _, provider := range providers {
		policy := RoutingPolicy{}
		for _, route := range routes {
			if route.ProviderID == provider.ID && policy.Default == "" {
				policy.Default = route.ID
			}
		}
		snapshot.ProviderPolicies[provider.ID] = policy
	}
	if legacy.Official {
		legacy.Official = false
		snapshot.ProviderPolicies[OfficialProviderID] = legacy
	} else {
		if route, ok := snapshot.Route(legacy.Default); ok {
			policy := snapshot.ProviderPolicies[route.ProviderID]
			policy.Default = route.ID
			snapshot.ProviderPolicies[route.ProviderID] = policy
		}
		for field, ref := range map[string]string{
			"web_search": legacy.WebSearch,
			"explore":    legacy.Subagents.Explore,
			"plan":       legacy.Subagents.Plan,
		} {
			route, ok := snapshot.Route(ref)
			if !ok {
				continue
			}
			policy := snapshot.ProviderPolicies[route.ProviderID]
			switch field {
			case "web_search":
				policy.WebSearch = route.ID
			case "explore":
				policy.Subagents.Explore = route.ID
			case "plan":
				policy.Subagents.Plan = route.ID
			}
			snapshot.ProviderPolicies[route.ProviderID] = policy
		}
		if active != "" {
			policy := snapshot.ProviderPolicies[active]
			policy.DefaultReasoningEffort = legacy.DefaultReasoningEffort
			snapshot.ProviderPolicies[active] = policy
		}
	}
	snapshot.ActiveProviderID = active
	return snapshot
}

// applyLegacyProfileRouting copies routing-related fields from old profiles.json
// into the active provider's remembered policy exactly once.
func applyLegacyProfileRouting(profileStore *profiles.Store, snapshot *Snapshot) bool {
	legacy, err := profileStore.ReadLegacyRoutingFields()
	if err != nil || legacy == nil || snapshot.ActiveProviderID == "" || snapshot.ActiveProviderID == OfficialProviderID {
		return false
	}
	policy := snapshot.ProviderPolicies[snapshot.ActiveProviderID]
	migrated := false
	translate := func(name string) string {
		route, ok := snapshot.Route(name)
		if ok && route.ProviderID == snapshot.ActiveProviderID {
			return route.ID
		}
		return ""
	}
	if policy.WebSearch == "" {
		if ref := translate(legacy.WebSearch); ref != "" {
			policy.WebSearch = ref
			migrated = true
		}
	}
	if policy.Subagents.Explore == "" {
		if ref := translate(legacy.SubagentsExplore); ref != "" {
			policy.Subagents.Explore = ref
			migrated = true
		}
	}
	if policy.Subagents.Plan == "" {
		if ref := translate(legacy.SubagentsPlan); ref != "" {
			policy.Subagents.Plan = ref
			migrated = true
		}
	}
	if migrated {
		snapshot.ProviderPolicies[snapshot.ActiveProviderID] = policy
	}
	return migrated
}

func policyWithRouteNames(snapshot Snapshot, policy RoutingPolicy) RoutingPolicy {
	name := func(ref string) string {
		if route, ok := snapshot.Route(ref); ok {
			return route.Name
		}
		return ref
	}
	policy.Default = name(policy.Default)
	policy.WebSearch = name(policy.WebSearch)
	policy.Subagents.Explore = name(policy.Subagents.Explore)
	policy.Subagents.Plan = name(policy.Subagents.Plan)
	return policy
}

// PersistedEqual reports whether two snapshots have the same durable routing
// state. Runtime hydration, compatibility mirrors, timestamps, and derived
// capability flags such as WebSearchCapable are ignored.
func PersistedEqual(left, right Snapshot) bool {
	left = sanitizedSnapshot(left)
	right = sanitizedSnapshot(right)
	left.UpdatedAt = time.Time{}
	right.UpdatedAt = time.Time{}
	left.Policy = RoutingPolicy{}
	right.Policy = RoutingPolicy{}
	// WebSearchCapable is recomputed from the selected route; it must not make a
	// healthy catalog look like it needs repair or force a rewrite on startup.
	for providerID, policy := range left.ProviderPolicies {
		policy.WebSearchCapable = false
		left.ProviderPolicies[providerID] = policy
	}
	for providerID, policy := range right.ProviderPolicies {
		policy.WebSearchCapable = false
		right.ProviderPolicies[providerID] = policy
	}
	return reflect.DeepEqual(left, right)
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	out := snapshot
	out.Providers = append([]Provider(nil), snapshot.Providers...)
	out.ModelRoutes = make([]ModelRoute, len(snapshot.ModelRoutes))
	for i, route := range snapshot.ModelRoutes {
		out.ModelRoutes[i] = route
		out.ModelRoutes[i].ExtraHeaders = cloneMap(route.ExtraHeaders)
		out.ModelRoutes[i].ReasoningEfforts = append([]string(nil), route.ReasoningEfforts...)
	}
	out.ProviderPolicies = make(map[string]RoutingPolicy, len(snapshot.ProviderPolicies))
	for providerID, policy := range snapshot.ProviderPolicies {
		out.ProviderPolicies[providerID] = policy
	}
	return out
}
