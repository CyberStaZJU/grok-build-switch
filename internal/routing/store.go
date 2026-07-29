package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	snapshot.Policy = policy
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
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	if err := s.writeLocked(snapshot); err != nil {
		return Snapshot{}, err
	}
	return cloneSnapshot(sanitizedSnapshot(snapshot)), nil
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
	if snapshot.Version == 0 {
		snapshot.Version = CurrentVersion
		dirty = true
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, false, false, fmt.Errorf("validate routing: %w", err)
	}
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

// applyLegacyProfileRouting copies routing-related fields from an old
// profiles.json into the routing policy when they are not already set.
// Returns true if any field was migrated.
func applyLegacyProfileRouting(profileStore *profiles.Store, snapshot *Snapshot) bool {
	legacy, err := profileStore.ReadLegacyRoutingFields()
	if err != nil || legacy == nil {
		return false
	}
	migrated := false
	if snapshot.Policy.WebSearch == "" && legacy.WebSearch != "" {
		if _, ok := snapshot.Route(legacy.WebSearch); ok {
			snapshot.Policy.WebSearch = legacy.WebSearch
			migrated = true
		}
	}
	if snapshot.Policy.Subagents.Explore == "" && legacy.SubagentsExplore != "" {
		if _, ok := snapshot.Route(legacy.SubagentsExplore); ok {
			snapshot.Policy.Subagents.Explore = legacy.SubagentsExplore
			migrated = true
		}
	}
	if snapshot.Policy.Subagents.Plan == "" && legacy.SubagentsPlan != "" {
		if _, ok := snapshot.Route(legacy.SubagentsPlan); ok {
			snapshot.Policy.Subagents.Plan = legacy.SubagentsPlan
			migrated = true
		}
	}
	return migrated
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
	return out
}
