//go:build darwin

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnableIsEnabledDisable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	exe := `/Applications/Grok & Switch <test>.app/Contents/MacOS/grok_switch`
	if err := Enable(exe, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "Library", "LaunchAgents", launchAgentName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	enabled, value, err := IsEnabled()
	if err != nil || !enabled {
		t.Fatalf("IsEnabled() = (%v, %q, %v)", enabled, value, err)
	}
	for _, expected := range []string{"Grok &amp; Switch &lt;test&gt;.app", "<string>--silent</string>", "<key>RunAtLoad</key>"} {
		if !strings.Contains(value, expected) {
			t.Errorf("plist missing %q", expected)
		}
	}
	if err := Disable(); err != nil {
		t.Fatal(err)
	}
	if err := Disable(); err != nil {
		t.Fatal(err)
	}
	enabled, _, err = IsEnabled()
	if err != nil || enabled {
		t.Fatalf("IsEnabled() after Disable = (%v, %v)", enabled, err)
	}
}
