package cliproxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	Version       = "7.2.94"
	Commit        = "36b45d57a3e804b9dfcee307e5d7b3e8cea5acfc"
	ArchiveName   = "CLIProxyAPI_7.2.94_darwin_aarch64.tar.gz"
	ArchiveSHA256 = "e3be2bc37e115a73a1a5bb11f67e6ddb72f313c4377261312b7551e58b428cef"
	BinarySHA256  = "4a93e141e942bdbd423462fa8b8726667bc243c5e0b093fa5802cbc47fa2601e"
	ArchiveSize   = int64(14243376)
	License       = "MIT"
	Label         = "com.grokbuildswitch.cliproxyapi"
	DefaultPort   = 8317
)

type Manifest struct {
	Version, Commit, Archive, SHA256, License string
	Size                                      int64
}

var BuiltinManifest = Manifest{Version, Commit, ArchiveName, ArchiveSHA256, License, ArchiveSize}

type Paths struct {
	Root, BinDir, Binary, Config, AuthDir, LogsDir, BackupDir, Stdout, Stderr string
}

func NewPaths(dataDir string) Paths {
	r := filepath.Join(dataDir, "cliproxy")
	return Paths{r, filepath.Join(r, "bin"), filepath.Join(r, "bin", "CLIProxyAPI"), filepath.Join(r, "config.yaml"), filepath.Join(r, "auth"), filepath.Join(r, "logs"), filepath.Join(r, "backup"), filepath.Join(r, "logs", "stdout.log"), filepath.Join(r, "logs", "stderr.log")}
}

func (p Paths) Ensure() error {
	for _, d := range []string{p.Root, p.BinDir, p.AuthDir, p.LogsDir, p.BackupDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func VerifyBinary(path, wantHash string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), wantHash) {
		return fmt.Errorf("CLIProxyAPI 校验失败")
	}
	mf, err := macho.Open(path)
	if err != nil {
		return fmt.Errorf("CLIProxyAPI 不是有效 Mach-O")
	}
	defer mf.Close()
	if mf.Cpu != macho.CpuArm64 {
		return fmt.Errorf("CLIProxyAPI 架构不是 arm64")
	}
	return nil
}

// InstallBuiltin verifies the bundled executable before atomically replacing the installed copy.
func InstallBuiltin(source string, p Paths, wantHash string) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	if err := VerifyBinary(source, wantHash); err != nil {
		return err
	}
	if err := VerifyBinary(p.Binary, wantHash); err == nil {
		return os.Chmod(p.Binary, 0o700)
	}
	tmp, err := os.CreateTemp(p.BinDir, ".CLIProxyAPI-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	in, err := os.Open(source)
	if err != nil {
		tmp.Close()
		return err
	}
	_, copyErr := io.Copy(tmp, in)
	closeIn := in.Close()
	syncErr := tmp.Sync()
	chmodErr := tmp.Chmod(0o700)
	closeErr := tmp.Close()
	for _, e := range []error{copyErr, closeIn, syncErr, chmodErr, closeErr} {
		if e != nil {
			return e
		}
	}
	if err := VerifyBinary(tmpName, wantHash); err != nil {
		return err
	}
	if _, err := os.Stat(p.Binary); err == nil {
		backup := filepath.Join(p.BackupDir, "CLIProxyAPI.previous")
		_ = os.Remove(backup)
		if err := os.Rename(p.Binary, backup); err != nil {
			return err
		}
		_ = os.Chmod(backup, 0o700)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmpName, p.Binary); err != nil {
		return err
	}
	return os.Chmod(p.Binary, 0o700)
}

type KeyStore interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
}

const keyService = "com.grokbuildswitch.cliproxyapi"
const inferenceAccount = "inference-api-key"
const managementAccount = "management-api-key"

type Keys struct{ Inference, Management string }

func EnsureKeys(store KeyStore) (Keys, error) {
	inference, err := ensureKey(store, inferenceAccount)
	if err != nil {
		return Keys{}, fmt.Errorf("读取 inference 密钥失败")
	}
	management, err := ensureKey(store, managementAccount)
	if err != nil {
		return Keys{}, fmt.Errorf("读取 management 密钥失败")
	}
	return Keys{inference, management}, nil
}

func ensureKey(store KeyStore, account string) (string, error) {
	value, err := store.Get(keyService, account)
	if err == nil && value != "" {
		return value, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	value = hex.EncodeToString(b)
	if err := store.Set(keyService, account, value); err != nil {
		return "", err
	}
	return value, nil
}

var ErrNotFound = errors.New("key not found")

func WriteConfig(p Paths, keys Keys) error {
	if err := p.Ensure(); err != nil {
		return err
	}
	// Prefer an explicit proxy so OAuth token exchange reaches OpenAI even when
	// the process is launched without a shell-level proxy environment.
	proxyURL := strings.TrimSpace(os.Getenv("HTTPS_PROXY"))
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(os.Getenv("HTTP_PROXY"))
	}
	if proxyURL == "" {
		// Common local proxy ports used by Clash/Surge on this machine.
		for _, candidate := range []string{"http://127.0.0.1:7890", "http://127.0.0.1:7897", "socks5://127.0.0.1:7891"} {
			proxyURL = candidate
			break
		}
	}
	data := fmt.Sprintf(`host: 127.0.0.1
port: %d
remote-management:
  allow-remote: false
  secret-key: %q
  disable-control-panel: true
auth-dir: %q
api-keys:
  - %q
proxy-url: %q
debug: false
commercial-mode: true
logging-to-file: false
usage-statistics: false
`, DefaultPort, keys.Management, p.AuthDir, keys.Inference, proxyURL)
	return atomicWrite(p.Config, []byte(data), 0o600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func Healthy(ctx context.Context, client *http.Client) bool {
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8317/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
