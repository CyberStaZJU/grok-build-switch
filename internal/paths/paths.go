package paths

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const migrationMarker = ".migrated-from-dot-grok-switch"

type Paths struct {
	GrokConfig        string
	GrokHome          string
	DataDir           string
	ProfilesFile      string
	RoutingFile       string
	CollaborationFile string
	SettingsFile      string
	RemoteAccessFile  string
	LogFile           string
	legacyDataDir     string
	migrateLegacy     bool
}

func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	grokHome := os.Getenv("GROK_HOME")
	if grokHome == "" {
		grokHome = filepath.Join(home, ".grok")
	}
	grokConfig := os.Getenv("GROK_CONFIG")
	if grokConfig == "" {
		grokConfig = filepath.Join(grokHome, "config.toml")
	}
	legacyDataDir := filepath.Join(home, ".grok_switch")
	dataDir := os.Getenv("GROK_SWITCH_HOME")
	migrateLegacy := false
	if dataDir == "" {
		dataDir = legacyDataDir
		if runtime.GOOS == "darwin" {
			dataDir = filepath.Join(home, "Library", "Application Support", "Grok Build Switch")
			migrateLegacy = true
		}
	}
	return Paths{
		GrokConfig:        grokConfig,
		GrokHome:          grokHome,
		DataDir:           dataDir,
		ProfilesFile:      filepath.Join(dataDir, "profiles.json"),
		RoutingFile:       filepath.Join(dataDir, "routing.json"),
		CollaborationFile: filepath.Join(dataDir, "collaboration.json"),
		SettingsFile:      filepath.Join(dataDir, "settings.json"),
		RemoteAccessFile:  filepath.Join(dataDir, "remote_access.json"),
		LogFile:           filepath.Join(dataDir, "grok_switch.log"),
		legacyDataDir:     legacyDataDir,
		migrateLegacy:     migrateLegacy,
	}, nil
}

func (p Paths) Ensure() error {
	if p.migrateLegacy {
		if err := p.migrate(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(p.DataDir, 0o700); err != nil {
		return err
	}
	return os.Chmod(p.DataDir, 0o700)
}

func (p Paths) migrate() error {
	if _, err := os.Stat(p.DataDir); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	info, err := os.Stat(p.legacyDataDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("legacy data path is not a directory: %s", p.legacyDataDir)
	}
	if err := copyTree(p.legacyDataDir, p.DataDir); err != nil {
		return fmt.Errorf("migrate legacy data: %w", err)
	}
	marker := filepath.Join(p.DataDir, migrationMarker)
	if err := os.WriteFile(marker, []byte(p.legacyDataDir+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(marker, 0o600)
}

func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to migrate symbolic link: %s", path)
		}
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to migrate non-regular file: %s", path)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(dst)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
