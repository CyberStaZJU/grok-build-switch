package agentbridge

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveExecutableUsesDarwinUserBin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin-specific candidates")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	path := filepath.Join(home, ".local", "bin", "grok")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveExecutable("", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != path {
		t.Fatalf("resolved = %q, want %q", resolved, path)
	}
}

func TestResolveExecutableUsesOverride(t *testing.T) {
	name := "grok"
	if runtime.GOOS == "windows" {
		name = "grok.exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveExecutable(path, "")
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := filepath.Abs(path)
	if resolved != expected {
		t.Fatalf("resolved = %q, want %q", resolved, expected)
	}
}
