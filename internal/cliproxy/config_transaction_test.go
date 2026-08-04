package cliproxy

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestConfigTransactionRecoveryMatrix(t *testing.T) {
	beforeConfig := []byte("host: before\n")
	afterConfig := []byte("host: after\n")
	beforeOwnership := configOwnership{Version: configOwnershipVersion, Aliases: []ownedAliasIdentity{{Channel: "codex", Name: "before", Alias: "subscription/codex/before"}}}
	afterOwnership := configOwnership{Version: configOwnershipVersion, Aliases: []ownedAliasIdentity{{Channel: "codex", Name: "after", Alias: "subscription/codex/after"}}}

	for _, test := range []struct {
		name            string
		configAfter     bool
		ownershipAfter  bool
		wantConfigAfter bool
		wantOwnerAfter  bool
	}{
		{name: "both before aborts", configAfter: false, ownershipAfter: false, wantConfigAfter: false, wantOwnerAfter: false},
		{name: "config after finishes ledger", configAfter: true, ownershipAfter: false, wantConfigAfter: true, wantOwnerAfter: true},
		{name: "ledger after restores ledger", configAfter: false, ownershipAfter: true, wantConfigAfter: false, wantOwnerAfter: false},
		{name: "both after finalizes", configAfter: true, ownershipAfter: true, wantConfigAfter: true, wantOwnerAfter: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := NewPaths(t.TempDir())
			if err := p.Ensure(); err != nil {
				t.Fatal(err)
			}
			if err := atomicWrite(p.Config, beforeConfig, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := saveConfigOwnership(p, beforeOwnership); err != nil {
				t.Fatal(err)
			}
			transaction, err := beginConfigTransaction(p, beforeConfig, true, afterConfig, afterOwnership)
			if err != nil {
				t.Fatal(err)
			}
			if err := saveConfigTransaction(p, transaction); err != nil {
				t.Fatal(err)
			}
			if test.configAfter {
				if err := atomicWrite(p.Config, afterConfig, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.ownershipAfter {
				if err := atomicWrite(configOwnershipPath(p), transaction.OwnershipAfter.Data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverLocalConfigTransaction(p); err != nil {
				t.Fatal(err)
			}
			configRaw, _ := os.ReadFile(p.Config)
			if got := bytes.Equal(configRaw, afterConfig); got != test.wantConfigAfter {
				t.Fatalf("config after=%v want %v: %s", got, test.wantConfigAfter, configRaw)
			}
			ownershipRaw, _ := os.ReadFile(configOwnershipPath(p))
			if got := bytes.Equal(ownershipRaw, transaction.OwnershipAfter.Data); got != test.wantOwnerAfter {
				t.Fatalf("ownership after=%v want %v: %s", got, test.wantOwnerAfter, ownershipRaw)
			}
			if _, err := os.Stat(configTransactionPath(p)); !os.IsNotExist(err) {
				t.Fatalf("journal was not removed: %v", err)
			}
		})
	}
}

func TestConfigTransactionUnknownStateFailsClosedAndKeepsJournal(t *testing.T) {
	p := NewPaths(t.TempDir())
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	beforeConfig := []byte("host: before\n")
	afterConfig := []byte("host: after\n")
	beforeOwnership := configOwnership{Version: configOwnershipVersion}
	afterOwnership := configOwnership{Version: configOwnershipVersion, Aliases: []ownedAliasIdentity{{Channel: "codex", Name: "after", Alias: "subscription/codex/after"}}}
	if err := atomicWrite(p.Config, beforeConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveConfigOwnership(p, beforeOwnership); err != nil {
		t.Fatal(err)
	}
	transaction, err := beginConfigTransaction(p, beforeConfig, true, afterConfig, afterOwnership)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfigTransaction(p, transaction); err != nil {
		t.Fatal(err)
	}
	unknown := []byte("host: external-user-edit\n")
	if err := atomicWrite(p.Config, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverLocalConfigTransaction(p); err == nil || !strings.Contains(err.Error(), "未知外部变更") {
		t.Fatalf("unknown state error=%v", err)
	}
	if raw, _ := os.ReadFile(p.Config); !bytes.Equal(raw, unknown) {
		t.Fatalf("unknown config was overwritten: %s", raw)
	}
	if _, err := os.Stat(configTransactionPath(p)); err != nil {
		t.Fatalf("journal must remain for manual recovery: %v", err)
	}
}

func TestConfigTransactionRejectsCorruptOrUnsupportedJournal(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("{broken"),
		[]byte(`{"version":99}`),
	} {
		p := NewPaths(t.TempDir())
		if err := p.Ensure(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configTransactionPath(p), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := recoverLocalConfigTransaction(p); err == nil {
			t.Fatalf("invalid journal accepted: %s", raw)
		}
	}
}

func TestConfigOperationLockRejectsLiveOwnerAndRecoversDeadOwner(t *testing.T) {
	p := NewPaths(t.TempDir())
	unlock, err := acquireConfigOperationLock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireConfigOperationLock(p); err == nil {
		t.Fatal("second live lock acquisition must fail")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configOperationLockPath(p), []byte("2147483647\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, err = acquireConfigOperationLock(p)
	if err != nil {
		t.Fatalf("dead-owner lock was not recovered: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestYAMLSemanticSHA256IgnoresCommentsAndMappingOrder(t *testing.T) {
	one := []byte("# comment\na: 1\nb:\n  c: true\n")
	two := []byte("b: {c: true}\na: 1\n")
	oneHash, err := yamlSemanticSHA256(one)
	if err != nil {
		t.Fatal(err)
	}
	twoHash, err := yamlSemanticSHA256(two)
	if err != nil {
		t.Fatal(err)
	}
	if oneHash != twoHash {
		t.Fatalf("semantic hash changed across formatting: %s %s", oneHash, twoHash)
	}
}
