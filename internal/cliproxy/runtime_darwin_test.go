//go:build darwin

package cliproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	output []byte
	err    error
	calls  [][]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.output, f.err
}

func TestLaunchAgentXMLAndPermissions(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	r := Runtime{Paths: NewPaths(data), Home: home, Runner: &fakeRunner{}}
	if err := r.InstallAgent(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(r.PlistPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{Label, "<string>-config</string>", r.Paths.Config, r.Paths.Stdout, "<key>SuccessfulExit</key><false/>", "HTTPS_PROXY", "NO_PROXY"} {
		if !strings.Contains(text, want) {
			t.Errorf("plist 缺少 %q", want)
		}
	}
	if !strings.Contains(text, r.Paths.Binary) && !strings.Contains(text, r.Paths.Binary+".bin") {
		t.Errorf("plist 未包含 CLIProxyAPI 程序路径")
	}
	info, _ := os.Stat(r.PlistPath())
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("plist 权限 %o", info.Mode().Perm())
	}
	if filepath.Base(r.PlistPath()) == "com.grokbuildswitch.app.plist" {
		t.Fatal("覆盖了主应用 LaunchAgent")
	}
}

func TestRuntimeStatusBoundaries(t *testing.T) {
	runner := &fakeRunner{err: errors.New("not loaded")}
	r := Runtime{Paths: NewPaths(t.TempDir()), Home: t.TempDir(), Runner: runner}
	st, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Running {
		t.Fatal("未加载不应 running")
	}
	runner.err = nil
	st, err = r.Status(context.Background())
	if err != nil || !st.Running {
		t.Fatal("已加载应 running")
	}
}
