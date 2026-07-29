//go:build darwin

package cliproxy

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type DarwinKeychain struct{ Runner Runner }

func (k DarwinKeychain) runner() Runner {
	if k.Runner != nil {
		return k.Runner
	}
	return ExecRunner{}
}
func (k DarwinKeychain) Get(service, account string) (string, error) {
	out, err := k.runner().Run(context.Background(), "/usr/bin/security", "find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		return "", ErrNotFound
	}
	return strings.TrimSpace(string(out)), nil
}
func (k DarwinKeychain) Set(service, account, value string) error {
	_, err := k.runner().Run(context.Background(), "/usr/bin/security", "add-generic-password", "-U", "-s", service, "-a", account, "-w", value)
	if err != nil {
		return fmt.Errorf("写入系统钥匙串失败")
	}
	return nil
}

type Runtime struct {
	Paths  Paths
	Home   string
	Runner Runner
}
type Status struct {
	Running, Healthy, PortConflict bool
	PID                            int
}

func (r Runtime) runner() Runner {
	if r.Runner != nil {
		return r.Runner
	}
	return ExecRunner{}
}
func (r Runtime) PlistPath() string {
	return filepath.Join(r.Home, "Library", "LaunchAgents", Label+".plist")
}
func escape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func (r Runtime) programPath() string {
	// Prefer the real Mach-O binary. A proxychains shell wrapper can break Go's
	// HTTP_PROXY handling during OAuth token exchange (context deadline exceeded).
	bin := r.Paths.Binary + ".bin"
	if info, err := os.Stat(bin); err == nil && !info.IsDir() {
		return bin
	}
	return r.Paths.Binary
}

func (r Runtime) plist() []byte {
	p := r.Paths
	proxy := localProxyURL()
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>-config</string><string>%s</string></array>
<key>WorkingDirectory</key><string>%s</string>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
<key>EnvironmentVariables</key><dict>
  <key>HTTPS_PROXY</key><string>%s</string>
  <key>HTTP_PROXY</key><string>%s</string>
  <key>ALL_PROXY</key><string>%s</string>
  <key>NO_PROXY</key><string>127.0.0.1,localhost</string>
</dict>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
</dict></plist>
`, Label, escape(r.programPath()), escape(p.Config), escape(p.Root), escape(p.Stdout), escape(p.Stderr), escape(proxy), escape(proxy), escape(proxy)))
}
func (r Runtime) InstallAgent() error {
	if err := r.Paths.Ensure(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.PlistPath()), 0o700); err != nil {
		return err
	}
	return atomicWrite(r.PlistPath(), r.plist(), 0o600)
}
func (r Runtime) domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }
func (r Runtime) Start(ctx context.Context) error {
	if err := r.InstallAgent(); err != nil {
		return err
	}
	_ = truncate(r.Paths.Stdout)
	_ = truncate(r.Paths.Stderr)
	_, err := r.runner().Run(ctx, "/bin/launchctl", "bootstrap", r.domain(), r.PlistPath())
	if err != nil {
		st, _ := r.Status(ctx)
		if st.Running {
			return nil
		}
		return fmt.Errorf("启动 CLIProxyAPI 失败")
	}
	return nil
}
func (r Runtime) Stop(ctx context.Context) error {
	_, err := r.runner().Run(ctx, "/bin/launchctl", "bootout", r.domain()+"/"+Label)
	if err != nil {
		st, _ := r.Status(ctx)
		if !st.Running {
			return nil
		}
		return fmt.Errorf("停止 CLIProxyAPI 失败")
	}
	// bootout can return before launchd has fully removed the job and released
	// port 8317. Wait briefly so a following restart cannot race bootstrap.
	for attempt := 0; attempt < 20; attempt++ {
		st, _ := r.Status(ctx)
		if !st.Running {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("停止 CLIProxyAPI 超时")
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("停止 CLIProxyAPI 超时")
}
func (r Runtime) Restart(ctx context.Context) error {
	if err := r.Stop(ctx); err != nil {
		return err
	}
	return r.Start(ctx)
}
func (r Runtime) Status(ctx context.Context) (Status, error) {
	out, err := r.runner().Run(ctx, "/bin/launchctl", "print", r.domain()+"/"+Label)
	running := err == nil
	healthy := Healthy(ctx, nil)
	pid := parseLaunchctlPID(string(out))
	return Status{Running: running, Healthy: running && healthy, PortConflict: !running && healthy, PID: pid}, nil
}

// parseLaunchctlPID extracts the PID from `launchctl print` output.
// Looks for a line like "pid = 12345" in the service state.
func parseLaunchctlPID(output string) int {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pid = ") {
			s := strings.TrimSpace(strings.TrimPrefix(line, "pid = "))
			s = strings.TrimSuffix(s, ",")
			if pid, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				return pid
			}
		}
	}
	return 0
}
func truncate(path string) error {
	if info, err := os.Stat(path); err == nil && info.Size() <= 1<<20 {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
