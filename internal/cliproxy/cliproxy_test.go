package cliproxy

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grok_switch/internal/modelvariants"

	"gopkg.in/yaml.v3"
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
	for _, want := range []string{"host: 127.0.0.1", "port: 8317", "allow-remote: false", "disable-control-panel: true", filepath.ToSlash(p.AuthDir), "commercial-mode: true", "logging-to-file: false", "usage-statistics-enabled: false"} {
		if !strings.Contains(filepath.ToSlash(text), want) {
			t.Errorf("配置缺少 %q", want)
		}
	}
	info, _ := os.Stat(p.Config)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("配置权限 %o", info.Mode().Perm())
	}
	ledgerInfo, err := os.Stat(configOwnershipPath(p))
	if err != nil || ledgerInfo.Mode().Perm() != 0o600 {
		t.Fatalf("ownership 权限错误: %v %v", ledgerInfo, err)
	}
	for _, d := range []string{p.Root, p.BinDir, p.AuthDir, p.LogsDir, p.BackupDir} {
		info, err := os.Stat(d)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("目录权限错误: %s", d)
		}
	}
}

func TestWriteConfigPreservesExistingYAMLAndManagedState(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	standard, _ := modelvariants.CodexStandardAlias("gpt-5.6-sol")
	fast, _ := modelvariants.CodexFastAlias("gpt-5.6-sol")
	ownership := configOwnership{
		Version: configOwnershipVersion,
		Aliases: []ownedAliasIdentity{
			{Channel: "codex", Name: "gpt-5.6-sol", Alias: standard},
			{Channel: "codex", Name: "gpt-5.6-sol", Alias: fast},
		},
		FastRuleFingerprint: fastRuleFingerprint([]string{fast}),
	}
	if err := saveConfigOwnership(p, ownership); err != nil {
		t.Fatal(err)
	}
	original := []byte(`# user comment
host: 0.0.0.0
custom:
  nested: keep-me
remote-management:
  future-setting: keep-me
oauth-model-alias:
  kimi:
    - name: moonshot-v1
      alias: user/kimi
      fork: true
payload:
  override:
    - models:
        - name: user-rule
          protocol: codex
      params:
        service_tier: priority
`)
	if err := os.WriteFile(p.Config, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(p, Keys{Inference: "infer-secret", Management: "manage-secret"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"# user comment", "custom:", "nested: keep-me", "future-setting: keep-me", "user/kimi", "user-rule", standard, fast, "host: 127.0.0.1"} {
		if !strings.Contains(text, want) {
			t.Errorf("持久配置缺少 %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "service_tier: priority") != 2 {
		t.Fatalf("用户规则或 Switch Fast 规则丢失:\n%s", text)
	}
	assertManagedConfigSemantics(t, raw, managedConfigFromOwnership(ownership))
}

func TestWriteConfigLegacyMigrationClaimsOnlyHistoricalShape(t *testing.T) {
	p := NewPaths(t.TempDir())
	aliases := oauthModelAliases{
		"codex": {
			{Name: "gpt-5.6-terra", Alias: "subscription/codex/gpt-5.6-terra", Fork: true, DisplayName: "gpt-5.6-terra"},
			{Name: "user-owned", Alias: "subscription/codex/user-owned", Fork: false},
			{Name: "forced", Alias: "subscription/codex/forced", Fork: true, ForceMapping: true},
		},
		"kimi": {{Name: "moonshot-v1", Alias: "user/kimi", Fork: true}},
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := saveModelAliases(p, aliases); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(p, Keys{Inference: "infer-secret", Management: "manage-secret"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p.Config)
	text := string(raw)
	standard, _ := modelvariants.CodexStandardAlias("gpt-5.6-terra")
	fast, _ := modelvariants.CodexFastAlias("gpt-5.6-terra")
	for _, want := range []string{standard, fast, "subscription/codex/user-owned", "subscription/codex/forced", "user/kimi"} {
		if !strings.Contains(text, want) {
			t.Fatalf("legacy startup state missing %q:\n%s", want, text)
		}
	}
	ownership, exists, err := loadConfigOwnership(p)
	if err != nil || !exists {
		t.Fatalf("ownership load: %+v %v %v", ownership, exists, err)
	}
	if len(ownership.Aliases) != 2 {
		t.Fatalf("only trusted historical standard + generated Fast should be owned: %+v", ownership.Aliases)
	}
	for _, identity := range ownership.Aliases {
		if identity.Alias != standard && identity.Alias != fast {
			t.Fatalf("legacy migration claimed user alias: %+v", identity)
		}
	}
	if strings.Count(text, aliasOwnershipMarker) != 2 {
		t.Fatalf("only historical Standard and generated Fast may be marked owned:\n%s", text)
	}
	// A second startup must preserve unowned legacy entries instead of deleting
	// them merely because the initial seed used the old sidecar.
	if err := WriteConfig(p, Keys{Inference: "infer-secret", Management: "manage-secret"}); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(p.Config)
	for _, want := range []string{"subscription/codex/user-owned", "subscription/codex/forced", "user/kimi"} {
		if !strings.Contains(string(second), want) {
			t.Fatalf("second startup removed unowned legacy alias %q:\n%s", want, second)
		}
	}
}

func TestWriteConfigMalformedYAMLDoesNotChangeOriginalOrLedger(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	original := []byte("host: [broken\n")
	if err := os.WriteFile(p.Config, original, 0o600); err != nil {
		t.Fatal(err)
	}
	ownership := configOwnership{Version: configOwnershipVersion}
	if err := saveConfigOwnership(p, ownership); err != nil {
		t.Fatal(err)
	}
	ledgerBefore, _ := os.ReadFile(configOwnershipPath(p))
	if err := WriteConfig(p, Keys{Inference: "infer-secret", Management: "manage-secret"}); err == nil {
		t.Fatal("应拒绝损坏 YAML")
	}
	raw, _ := os.ReadFile(p.Config)
	ledgerAfter, _ := os.ReadFile(configOwnershipPath(p))
	if !bytes.Equal(raw, original) || !bytes.Equal(ledgerBefore, ledgerAfter) {
		t.Fatalf("失败写入改变了原文件或 ledger: config=%q ledger=%q", raw, ledgerAfter)
	}
}

func TestWriteConfigUsesAuthoritativeUsageStatisticsKey(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	original := []byte(`# keep enabled comment
usage-statistics-enabled: true
usage-statistics: user-value
`)
	if err := os.WriteFile(p.Config, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(p, Keys{Inference: "infer-secret", Management: "manage-secret"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	_, root, err := parseYAMLRoot(raw)
	if err != nil {
		t.Fatal(err)
	}
	enabled, _, ok := mappingValue(root, "usage-statistics-enabled")
	if !ok || enabled.Tag != "!!bool" || enabled.Value != "false" {
		t.Fatalf("authoritative key not disabled: %s", raw)
	}
	legacy, _, ok := mappingValue(root, "usage-statistics")
	if !ok || legacy.Value != "user-value" {
		t.Fatalf("unowned legacy key was not preserved: %s", raw)
	}
	if !strings.Contains(string(raw), "# keep enabled comment") {
		t.Fatalf("managed key comment lost: %s", raw)
	}
}

func TestWriteConfigLegacySidecarFailuresFailClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		make func(t *testing.T, path string)
	}{
		{name: "malformed", make: func(t *testing.T, path string) { t.Helper(); mustWrite(t, path, []byte("{broken")) }},
		{name: "trailing document", make: func(t *testing.T, path string) { t.Helper(); mustWrite(t, path, []byte("{} {}")) }},
		{name: "null", make: func(t *testing.T, path string) { t.Helper(); mustWrite(t, path, []byte("null")) }},
		{name: "directory", make: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := NewPaths(t.TempDir())
			if err := p.Ensure(); err != nil {
				t.Fatal(err)
			}
			test.make(t, modelAliasesPath(p))
			if err := WriteConfig(p, Keys{Inference: "infer-secret", Management: "manage-secret"}); err == nil {
				t.Fatal("invalid legacy sidecar must fail closed")
			}
			if _, err := os.Stat(p.Config); !os.IsNotExist(err) {
				t.Fatalf("failed migration created config: %v", err)
			}
			if _, err := os.Stat(configOwnershipPath(p)); !os.IsNotExist(err) {
				t.Fatalf("failed migration created ownership ledger: %v", err)
			}
		})
	}
}

func TestWriteConfigExistingLedgerIgnoresMalformedLegacySidecar(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigOwnership(p, configOwnership{Version: configOwnershipVersion}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, modelAliasesPath(p), []byte("{broken"))
	if err := WriteConfig(p, Keys{Inference: "infer-secret", Management: "manage-secret"}); err != nil {
		t.Fatalf("explicit ledger must end one-time legacy migration: %v", err)
	}
}

func TestMergeRejectsCaseEquivalentAndNonCanonicalAliasChannels(t *testing.T) {
	previous := configOwnership{Version: configOwnershipVersion}
	desired := generatedManagedConfig([]upstreamModel{{ID: "gpt-5.6-luna", OwnedBy: "codex"}})
	for _, raw := range []string{
		"oauth-model-alias:\n  codex: []\n  Codex: []\n",
		"oauth-model-alias:\n  Codex: []\n",
		"oauth-model-alias:\n  Vendor: []\n  vendor: []\n",
	} {
		if _, _, err := mergeManagedConfig([]byte(raw), desired, previous, nil); err == nil {
			t.Fatalf("ambiguous/noncanonical channel accepted:\n%s", raw)
		}
	}
	unknown := []byte("oauth-model-alias:\n  Vendor:\n    - name: custom\n      alias: vendor/custom\n      fork: true\n")
	merged, _, err := mergeManagedConfig(unknown, managedConfig{Aliases: oauthModelAliases{}}, previous, nil)
	if err != nil || !strings.Contains(string(merged), "Vendor:") {
		t.Fatalf("single unknown mixed-case channel should be preserved: %v\n%s", err, merged)
	}
}

func TestMergeRejectsUnauthenticatedAliasOwnershipMarkers(t *testing.T) {
	standard, _ := modelvariants.CodexStandardAlias("gpt-5.6-luna")
	identity := ownedAliasIdentity{Channel: "codex", Name: "gpt-5.6-luna", Alias: standard}
	previous := configOwnership{Version: configOwnershipVersion, Aliases: []ownedAliasIdentity{identity}}
	desired := generatedManagedConfig([]upstreamModel{{ID: "gpt-5.6-luna", OwnedBy: "codex"}})
	for _, marker := range []string{aliasOwnershipMarker, aliasOwnershipMarker + ":" + strings.Repeat("0", 64)} {
		raw := []byte(fmt.Sprintf("oauth-model-alias:\n  codex:\n    # %s\n    - name: gpt-5.6-luna\n      alias: %s\n      fork: true\n      display-name: gpt-5.6-luna\n", marker, standard))
		if _, _, err := mergeManagedConfig(raw, desired, previous, nil); err == nil {
			t.Fatalf("unauthenticated marker accepted: %s", marker)
		}
	}
}

func TestMergeAuthenticatedAliasMarkerAndCommentLossFallback(t *testing.T) {
	standard, _ := modelvariants.CodexStandardAlias("gpt-5.6-luna")
	identity := ownedAliasIdentity{Channel: "codex", Name: "gpt-5.6-luna", Alias: standard}
	previous := configOwnership{Version: configOwnershipVersion, Aliases: []ownedAliasIdentity{identity}}
	desired := generatedManagedConfig([]upstreamModel{{ID: "gpt-5.6-luna", OwnedBy: "codex"}})
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	aliasMap := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{aliasYAMLNode(ownedAliasFromIdentity(identity))}}
	aliasMap.Content = append(aliasMap.Content, stringNode("codex"), sequence)
	root.Content = append(root.Content, stringNode("oauth-model-alias"), aliasMap)
	markedRaw, err := encodeYAML(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mergeManagedConfig(markedRaw, desired, previous, nil); err != nil {
		t.Fatalf("authenticated marker rejected: %v", err)
	}
	commentFree := []byte(fmt.Sprintf("oauth-model-alias:\n  codex:\n    - name: gpt-5.6-luna\n      alias: %s\n      fork: true\n      display-name: gpt-5.6-luna\n", standard))
	if _, _, err := mergeManagedConfig(commentFree, desired, previous, nil); err != nil {
		t.Fatalf("exact comment-loss fallback rejected: %v", err)
	}
}

func TestMergeRejectsUnauthenticatedFastRuleMarker(t *testing.T) {
	fast, _ := modelvariants.CodexFastAlias("gpt-5.6-luna")
	previous := configOwnership{Version: configOwnershipVersion, FastRuleFingerprint: fastRuleFingerprint([]string{fast})}
	desired := generatedManagedConfig([]upstreamModel{{ID: "gpt-5.6-luna", OwnedBy: "codex"}})
	raw := []byte(fmt.Sprintf("payload:\n  override:\n    # %s\n    - models:\n        - name: %s\n          protocol: codex\n      params:\n        service_tier: priority\n", fastOwnershipMarker, fast))
	if _, _, err := mergeManagedConfig(raw, desired, previous, nil); err == nil {
		t.Fatal("legacy/copied Fast marker must not authorize deletion")
	}
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMergeRejectsUnownedDesiredAliasConflict(t *testing.T) {
	previous := configOwnership{Version: configOwnershipVersion}
	desired := generatedManagedConfig([]upstreamModel{{ID: "gpt-5.6-luna", OwnedBy: "codex"}})
	raw := []byte(`oauth-model-alias:
  codex:
    - name: user-physical
      alias: subscription/codex/gpt-5.6-luna
      fork: true
`)
	if _, _, err := mergeManagedConfig(raw, desired, previous, nil); err == nil || !strings.Contains(err.Error(), "已由用户配置占用") {
		t.Fatalf("unowned alias conflict not rejected: %v", err)
	}
}

func TestMergeRejectsDuplicateAliasChannels(t *testing.T) {
	previous := configOwnership{Version: configOwnershipVersion}
	desired := generatedManagedConfig([]upstreamModel{{ID: "gpt-5.6-luna", OwnedBy: "codex"}})
	raw := []byte(`oauth-model-alias:
  codex: []
  codex: []
`)
	if _, _, err := mergeManagedConfig(raw, desired, previous, nil); err == nil {
		t.Fatal("duplicate alias channel should fail closed")
	}
}

func TestMergeReplacesOnlyLastLedgerOwnedDuplicate(t *testing.T) {
	previous := configOwnership{Version: configOwnershipVersion, Aliases: []ownedAliasIdentity{{Channel: "codex", Name: "gpt-5.6-luna", Alias: "subscription/codex/gpt-5.6-luna"}}}
	desired := generatedManagedConfig([]upstreamModel{{ID: "gpt-5.6-luna", OwnedBy: "codex"}})
	raw := []byte(`oauth-model-alias:
  codex:
    - name: gpt-5.6-luna
      alias: subscription/codex/gpt-5.6-luna
      fork: true
      display-name: user-copy
    - name: gpt-5.6-luna
      alias: subscription/codex/gpt-5.6-luna
      fork: true
      display-name: gpt-5.6-luna
`)
	merged, _, err := mergeManagedConfig(raw, desired, previous, nil)
	if err == nil || !strings.Contains(err.Error(), "已由用户配置占用") {
		t.Fatalf("duplicate user identity should prevent takeover after only last owned entry is removed: %v\n%s", err, merged)
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
