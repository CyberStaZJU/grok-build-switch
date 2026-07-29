package recovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupCorruptPreservesOriginalBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	want := []byte("{broken json")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	backup, err := BackupCorrupt(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original path still exists or stat failed: %v", err)
	}
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("backup content = %q, want %q", got, want)
	}
}
