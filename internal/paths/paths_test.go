package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_SWITCH_HOME", "")

	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".grok_switch")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(home, "Library", "Application Support", "Grok Build Switch")
	}
	if got.DataDir != want {
		t.Fatalf("DataDir = %q, want %q", got.DataDir, want)
	}
}

func TestResolveDataDirOverride(t *testing.T) {
	home := t.TempDir()
	override := filepath.Join(t.TempDir(), "custom")
	t.Setenv("HOME", home)
	t.Setenv("GROK_SWITCH_HOME", override)

	got, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != override {
		t.Fatalf("DataDir = %q, want %q", got.DataDir, override)
	}
}

func TestEnsureMigratesLegacyDataOnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-specific migration")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GROK_SWITCH_HOME", "")
	legacy := filepath.Join(home, ".grok_switch")
	if err := os.MkdirAll(filepath.Join(legacy, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "nested", "secret.json"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "nested", "secret.json")); err != nil {
		t.Fatalf("legacy data was modified: %v", err)
	}
	copied := filepath.Join(paths.DataDir, "nested", "secret.json")
	if data, err := os.ReadFile(copied); err != nil || string(data) != "secret" {
		t.Fatalf("copied data = %q, err = %v", data, err)
	}
	assertMode(t, paths.DataDir, 0o700)
	assertMode(t, filepath.Join(paths.DataDir, "nested"), 0o700)
	assertMode(t, copied, 0o600)
	assertMode(t, filepath.Join(paths.DataDir, migrationMarker), 0o600)
}

func TestEnsureDoesNotMigrateIntoOverride(t *testing.T) {
	home := t.TempDir()
	override := filepath.Join(t.TempDir(), "custom")
	t.Setenv("HOME", home)
	t.Setenv("GROK_SWITCH_HOME", override)
	legacy := filepath.Join(home, ".grok_switch")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "settings.json"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	paths, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(override, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("override unexpectedly received legacy data: %v", err)
	}
	assertMode(t, override, 0o700)
	if _, err := os.Stat(filepath.Join(override, "backups")); !os.IsNotExist(err) {
		t.Fatalf("Ensure() unexpectedly created backups directory: %v", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %o, want %o", path, got, want)
	}
}
