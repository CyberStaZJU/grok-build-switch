package cliproxy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeKeys map[string]string

func (f fakeKeys) Get(_, account string) (string, error) {
	v := f[account]
	if v == "" {
		return "", ErrNotFound
	}
	return v, nil
}
func (f fakeKeys) Set(_, account, value string) error { f[account] = value; return nil }

func TestManifest(t *testing.T) {
	if BuiltinManifest.Version != "7.2.94" || BuiltinManifest.Commit != Commit || BuiltinManifest.Size != 14243376 || len(BuiltinManifest.SHA256) != 64 {
		t.Fatal("manifest 不匹配")
	}
}

func TestEnsureKeysStable(t *testing.T) {
	store := fakeKeys{}
	one, err := EnsureKeys(store)
	if err != nil {
		t.Fatal(err)
	}
	two, err := EnsureKeys(store)
	if err != nil {
		t.Fatal(err)
	}
	if one != two || one.Inference == one.Management || len(one.Inference) != 64 {
		t.Fatal("密钥生成不稳定或不独立")
	}
}

func TestWriteConfigPermissions(t *testing.T) {
	p := NewPaths(t.TempDir())
	keys := Keys{Inference: "infer-secret", Management: "manage-secret"}
	if err := WriteConfig(p, keys); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"host: 127.0.0.1", "port: 8317", "allow-remote: false", "disable-control-panel: true", filepath.ToSlash(p.AuthDir), "commercial-mode: true", "logging-to-file: false", "usage-statistics: false"} {
		if !strings.Contains(filepath.ToSlash(text), want) {
			t.Errorf("配置缺少 %q", want)
		}
	}
	info, _ := os.Stat(p.Config)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("配置权限 %o", info.Mode().Perm())
	}
	for _, d := range []string{p.Root, p.BinDir, p.AuthDir, p.LogsDir, p.BackupDir} {
		info, err := os.Stat(d)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("目录权限错误: %s", d)
		}
	}
}

func TestConfigErrorDoesNotLeakKeys(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := os.WriteFile(p.Root, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys := Keys{Inference: "infer-secret", Management: "manage-secret"}
	err := WriteConfig(p, keys)
	if err == nil || strings.Contains(err.Error(), keys.Inference) || strings.Contains(err.Error(), keys.Management) {
		t.Fatal("错误为空或泄漏密钥")
	}
}

func TestVerifyRejectsHashAndArchitecture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, []byte("not macho"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBinary(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("应拒绝错误 hash")
	}
	if err := VerifyBinary(path, "9c4eae1c075a59e40d495b94ad8f7ee7a8f98f61e1b205048c0c0a8490c67f84"); err == nil {
		t.Fatal("应拒绝非 Mach-O")
	}
}

func TestEnsureKeysSanitizesStoreError(t *testing.T) {
	bad := failingKeys{}
	_, err := EnsureKeys(bad)
	if err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatal("错误泄漏密钥")
	}
}

type failingKeys struct{}

func (failingKeys) Get(string, string) (string, error) { return "", errors.New("super-secret") }
func (failingKeys) Set(string, string, string) error   { return nil }
