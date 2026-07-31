package profiles

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"grok_switch/internal/recovery"
)

type Store struct {
	path        string
	mu          sync.Mutex
	idGenerator func() (string, error)
}

func NewStore(path string) *Store {
	return &Store{path: path, idGenerator: newID}
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) List() ([]Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *Store) Get(id string) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profiles, err := s.readLocked()
	if err != nil {
		return Profile{}, err
	}
	for _, profile := range profiles {
		if profile.ID == id {
			return profile, nil
		}
	}
	return Profile{}, os.ErrNotExist
}

func (s *Store) Create(profile Profile) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profiles, err := s.readLocked()
	if err != nil {
		return Profile{}, err
	}
	profile.ID, err = s.uniqueIDLocked(profiles)
	if err != nil {
		return Profile{}, err
	}
	now := time.Now()
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = now
	}
	profile = Normalize(profile)
	if err := ValidateDefaultReasoningEffort(profile); err != nil {
		return Profile{}, err
	}
	profile.UpdatedAt = now
	profiles = append(profiles, profile)
	if err := s.writeLocked(profiles); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func (s *Store) Update(id string, next Profile) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profiles, err := s.readLocked()
	if err != nil {
		return Profile{}, err
	}
	for i := range profiles {
		if profiles[i].ID == id {
			next.ID = id
			next.CreatedAt = profiles[i].CreatedAt
			next.UpdatedAt = time.Now()
			next = Normalize(next)
			if err := ValidateDefaultReasoningEffort(next); err != nil {
				return Profile{}, err
			}
			profiles[i] = next
			if err := s.writeLocked(profiles); err != nil {
				return Profile{}, err
			}
			return next, nil
		}
	}
	return Profile{}, os.ErrNotExist
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	profiles, err := s.readLocked()
	if err != nil {
		return err
	}
	next := profiles[:0]
	found := false
	for _, profile := range profiles {
		if profile.ID == id {
			found = true
			continue
		}
		next = append(next, profile)
	}
	if !found {
		return os.ErrNotExist
	}
	return s.writeLocked(next)
}

func (s *Store) EnsureDir() error {
	return os.MkdirAll(filepath.Dir(s.path), 0o700)
}

// LegacyRoutingFields holds routing-related values that were previously stored
// on the Profile struct. These are now owned by the routing policy, but old
// profiles.json files may still carry them and need one-time migration.
type LegacyRoutingFields struct {
	WebSearch        string `json:"web_search_model"`
	SubagentsExplore string `json:"subagents_explore_model"`
	SubagentsPlan    string `json:"subagents_plan_model"`
}

// ReadLegacyRoutingFields reads the raw profiles.json and returns the legacy
// routing fields from the first profile that has any. Returns nil when no
// legacy fields are present or the file does not exist.
func (s *Store) ReadLegacyRoutingFields() (*LegacyRoutingFields, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var persisted []Profile
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, err
	}
	if err := validatePersistedIDs(persisted); err != nil {
		return nil, s.quarantineLocked(data, err)
	}
	var rawProfiles []map[string]any
	if err := json.Unmarshal(data, &rawProfiles); err != nil {
		return nil, err
	}
	for _, raw := range rawProfiles {
		fields := &LegacyRoutingFields{}
		hasAny := false
		if v, ok := raw["web_search_model"].(string); ok && v != "" {
			fields.WebSearch = v
			hasAny = true
		}
		if sub, ok := raw["subagents_models"].(map[string]any); ok {
			if v, ok := sub["explore"].(string); ok && v != "" {
				fields.SubagentsExplore = v
				hasAny = true
			}
			if v, ok := sub["plan"].(string); ok && v != "" {
				fields.SubagentsPlan = v
				hasAny = true
			}
		}
		if v, ok := raw["subagents_default_model"].(string); ok && v != "" {
			if fields.SubagentsExplore == "" {
				fields.SubagentsExplore = v
				hasAny = true
			}
			if fields.SubagentsPlan == "" {
				fields.SubagentsPlan = v
				hasAny = true
			}
		}
		if hasAny {
			return fields, nil
		}
	}
	return nil, nil
}

func (s *Store) readLocked() ([]Profile, error) {
	if err := s.EnsureDir(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Profile{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []Profile{}, nil
	}
	var profiles []Profile
	if err := json.Unmarshal(data, &profiles); err != nil {
		cause := fmt.Errorf("read profiles: %w", err)
		backup, backupErr := recovery.BackupCorrupt(s.path)
		if backupErr != nil {
			return nil, fmt.Errorf("%v; backup corrupt profiles: %w", cause, backupErr)
		}
		log.Printf("recovered profiles file %s after %v; backup=%s", s.path, cause, backup)
		profiles = []Profile{}
		if writeErr := s.writeLocked(profiles); writeErr != nil {
			return nil, fmt.Errorf("restore empty profiles after %v: %w", cause, writeErr)
		}
		return profiles, nil
	}
	if err := validatePersistedIDs(profiles); err != nil {
		return nil, s.quarantineLocked(data, err)
	}
	for i := range profiles {
		profiles[i] = Normalize(profiles[i])
	}
	return profiles, nil
}

func validatePersistedIDs(profiles []Profile) error {
	seen := make(map[string]struct{}, len(profiles))
	for i, profile := range profiles {
		if profile.ID == "" {
			return fmt.Errorf("profile at index %d has empty id", i)
		}
		if _, exists := seen[profile.ID]; exists {
			return fmt.Errorf("duplicate profile id %q", profile.ID)
		}
		seen[profile.ID] = struct{}{}
	}
	return nil
}

func (s *Store) quarantineLocked(original []byte, cause error) error {
	backup, err := recovery.BackupCorrupt(s.path)
	if err != nil {
		return fmt.Errorf("invalid profile identities: %v; quarantine profiles: %w", cause, err)
	}
	preserved, err := os.ReadFile(backup)
	if err != nil {
		return fmt.Errorf("invalid profile identities: %v; verify quarantine %s: %w", cause, backup, err)
	}
	if !bytes.Equal(preserved, original) {
		return fmt.Errorf("invalid profile identities: %v; quarantine %s did not preserve original bytes", cause, backup)
	}
	log.Printf("quarantined profiles file %s after invalid profile identities: %v; backup=%s", s.path, cause, backup)
	return fmt.Errorf("invalid profile identities: %v; profiles quarantined at %s", cause, backup)
}

func (s *Store) uniqueIDLocked(profiles []Profile) (string, error) {
	used := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		used[profile.ID] = struct{}{}
	}
	for attempts := 0; attempts < 128; attempts++ {
		id, err := s.idGenerator()
		if err != nil {
			return "", fmt.Errorf("generate profile id: %w", err)
		}
		if id == "" {
			continue
		}
		if _, exists := used[id]; !exists {
			return id, nil
		}
	}
	return "", fmt.Errorf("generate unique profile id: exhausted attempts")
}

func (s *Store) writeLocked(profiles []Profile) error {
	if err := validatePersistedIDs(profiles); err != nil {
		return fmt.Errorf("write profiles: %w", err)
	}
	data, err := json.MarshalIndent(profiles, "", "  ")
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
	if _, err := tmp.Write(data); err != nil {
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
			return os.Rename(tmpName, path)
		}
		return err
	}
	return nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
