package switcher

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	grokconfig "grok_switch/internal/config"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
)

type Switcher struct {
	ConfigPath string
	Profiles   *profiles.Store
	mu         sync.Mutex
}

func (s *Switcher) Activate(id string) (profiles.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	profile, err := s.Profiles.Get(id)
	if err != nil {
		return profiles.Profile{}, err
	}
	if err := profiles.ValidateDefaultReasoningEffort(profile); err != nil {
		return profiles.Profile{}, fmt.Errorf("无法启用：%w", err)
	}
	if err := grokconfig.ApplyProfileToFile(s.ConfigPath, profile); err != nil {
		return profiles.Profile{}, err
	}
	return profile, nil
}

func (s *Switcher) ActivateOfficial() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return grokconfig.UseOfficialAuthToFile(s.ConfigPath)
}

func (s *Switcher) ApplyPrivacyProtection() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return grokconfig.ApplyPrivacyProtectionToFile(s.ConfigPath)
}

// ApplyRouting atomically applies a hydrated multi-provider routing snapshot
// while holding the switcher mutation lock.
func (s *Switcher) ApplyRouting(snapshot routing.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return grokconfig.ApplyRoutingToFile(s.ConfigPath, snapshot)
}

// RestoreConfigState is the rollback half of a failed routing transaction.
func (s *Switcher) RestoreConfigState(content []byte, existed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !existed {
		if err := os.Remove(s.ConfigPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return atomicWrite(s.ConfigPath, content)
}

func (s *Switcher) ImportCurrent(name string, _ bool) (profiles.Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	profile, err := grokconfig.ImportProfile(s.ConfigPath, name)
	if err != nil {
		return profiles.Profile{}, err
	}
	created, err := s.Profiles.Create(profile)
	if err != nil {
		return profiles.Profile{}, err
	}
	return created, nil
}

func (s *Switcher) EnsureDefaultProfile() error {
	profilesList, err := s.Profiles.List()
	if err != nil {
		return err
	}
	if len(profilesList) > 0 {
		return nil
	}
	if _, err := os.Stat(s.ConfigPath); err != nil {
		return err
	}
	_, err = s.ImportCurrent("Default", false)
	return err
}

func (s *Switcher) ActiveStatus() (profiles.Profile, bool, error) {
	profilesList, err := s.Profiles.List()
	if err != nil {
		return profiles.Profile{}, false, err
	}
	for _, profile := range profilesList {
		matches, err := grokconfig.CurrentMatches(s.ConfigPath, profile)
		if err != nil {
			return profiles.Profile{}, false, err
		}
		if matches {
			return profile, true, nil
		}
	}
	return profiles.Profile{}, false, nil
}

func (s *Switcher) ReadConfig() ([]byte, error) {
	data, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Switcher) WriteConfig(content []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicWrite(s.ConfigPath, content)
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
