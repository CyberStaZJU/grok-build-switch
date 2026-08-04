package cliproxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const configTransactionVersion = 1

type transactionFileSnapshot struct {
	Exists bool   `json:"exists"`
	Data   []byte `json:"data,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type configTransaction struct {
	Version                   int                     `json:"version"`
	ConfigBefore              transactionFileSnapshot `json:"config_before"`
	ConfigAfterSemanticSHA256 string                  `json:"config_after_semantic_sha256"`
	OwnershipBefore           transactionFileSnapshot `json:"ownership_before"`
	OwnershipAfter            transactionFileSnapshot `json:"ownership_after"`
}

type configReadFunc func() ([]byte, bool, error)
type configWriteFunc func([]byte, bool) error

func configTransactionPath(p Paths) string {
	return filepath.Join(p.Root, "config-transaction.json")
}

func configOperationLockPath(p Paths) string {
	return filepath.Join(p.Root, "config-operation.lock")
}

func acquireConfigOperationLock(p Paths) (func() error, error) {
	if err := p.Ensure(); err != nil {
		return nil, err
	}
	path := configOperationLockPath(p)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, err = fmt.Fprintf(file, "%d\n", os.Getpid()); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		return func() error { return durableRemove(path) }, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	raw, readErr := os.ReadFile(path)
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if readErr != nil || parseErr != nil || pid <= 0 || processExists(pid) {
		return nil, fmt.Errorf("另一个 Grok Build Switch 正在更新 CLIProxyAPI 配置")
	}
	// A dead owner left a stale lock. Remove only after proving that PID no
	// longer exists, then attempt one exclusive creation.
	if err := durableRemove(path); err != nil {
		return nil, err
	}
	return acquireConfigOperationLock(p)
}

func snapshotBytes(raw []byte, exists bool) transactionFileSnapshot {
	if !exists {
		return transactionFileSnapshot{}
	}
	copyRaw := append([]byte(nil), raw...)
	sum := sha256.Sum256(copyRaw)
	return transactionFileSnapshot{Exists: true, Data: copyRaw, SHA256: hex.EncodeToString(sum[:])}
}

func (snapshot transactionFileSnapshot) valid() bool {
	if !snapshot.Exists {
		return len(snapshot.Data) == 0 && snapshot.SHA256 == ""
	}
	sum := sha256.Sum256(snapshot.Data)
	return snapshot.SHA256 == hex.EncodeToString(sum[:])
}

func (snapshot transactionFileSnapshot) matches(raw []byte, exists bool) bool {
	if snapshot.Exists != exists {
		return false
	}
	if !exists {
		return true
	}
	return bytes.Equal(snapshot.Data, raw)
}

func loadConfigTransaction(p Paths) (configTransaction, bool, error) {
	raw, err := os.ReadFile(configTransactionPath(p))
	if errors.Is(err, os.ErrNotExist) {
		return configTransaction{}, false, nil
	}
	if err != nil {
		return configTransaction{}, true, fmt.Errorf("读取 CLIProxyAPI 配置事务失败")
	}
	var transaction configTransaction
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&transaction); err != nil || requireJSONEOF(decoder) != nil {
		return configTransaction{}, true, fmt.Errorf("CLIProxyAPI 配置事务记录无效")
	}
	if err := validateConfigTransaction(transaction); err != nil {
		return configTransaction{}, true, err
	}
	return transaction, true, nil
}

func saveConfigTransaction(p Paths, transaction configTransaction) error {
	if err := validateConfigTransaction(transaction); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(configTransactionPath(p), append(raw, '\n'), 0o600)
}

func validateConfigTransaction(transaction configTransaction) error {
	if transaction.Version != configTransactionVersion || !transaction.ConfigBefore.valid() || !transaction.OwnershipBefore.valid() || !transaction.OwnershipAfter.valid() || !transaction.OwnershipAfter.Exists {
		return fmt.Errorf("CLIProxyAPI 配置事务记录无效")
	}
	if !validSHA256(transaction.ConfigAfterSemanticSHA256) {
		return fmt.Errorf("CLIProxyAPI 配置事务记录无效")
	}
	if transaction.ConfigBefore.Exists {
		if _, err := yamlSemanticSHA256(transaction.ConfigBefore.Data); err != nil {
			return fmt.Errorf("CLIProxyAPI 配置事务记录无效")
		}
	}
	if transaction.OwnershipBefore.Exists {
		if _, err := decodeConfigOwnership(transaction.OwnershipBefore.Data); err != nil {
			return fmt.Errorf("CLIProxyAPI 配置事务记录无效")
		}
	}
	if _, err := decodeConfigOwnership(transaction.OwnershipAfter.Data); err != nil {
		return fmt.Errorf("CLIProxyAPI 配置事务记录无效")
	}
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == hex.EncodeToString(decoded)
}

func readFileForTransaction(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func localConfigReader(p Paths) configReadFunc {
	return func() ([]byte, bool, error) {
		return readFileForTransaction(p.Config)
	}
}

func localConfigWriter(p Paths) configWriteFunc {
	return func(raw []byte, exists bool) error {
		if !exists {
			return durableRemove(p.Config)
		}
		return atomicWrite(p.Config, raw, 0o600)
	}
}

func recoverLocalConfigTransaction(p Paths) error {
	return recoverConfigTransaction(p, localConfigReader(p), localConfigWriter(p))
}

func beginConfigTransaction(p Paths, beforeConfig []byte, beforeConfigExists bool, afterConfig []byte, afterOwnership configOwnership) (configTransaction, error) {
	beforeOwnershipRaw, beforeOwnershipExists, err := readFileForTransaction(configOwnershipPath(p))
	if err != nil {
		return configTransaction{}, err
	}
	afterOwnershipRaw, err := marshalConfigOwnership(afterOwnership)
	if err != nil {
		return configTransaction{}, err
	}
	afterConfigDigest, err := yamlSemanticSHA256(afterConfig)
	if err != nil {
		return configTransaction{}, err
	}
	transaction := configTransaction{
		Version:                   configTransactionVersion,
		ConfigBefore:              snapshotBytes(beforeConfig, beforeConfigExists),
		ConfigAfterSemanticSHA256: afterConfigDigest,
		OwnershipBefore:           snapshotBytes(beforeOwnershipRaw, beforeOwnershipExists),
		OwnershipAfter:            snapshotBytes(afterOwnershipRaw, true),
	}
	if err := validateConfigTransaction(transaction); err != nil {
		return configTransaction{}, err
	}
	return transaction, nil
}

func recoverConfigTransaction(p Paths, readConfig configReadFunc, _ configWriteFunc) error {
	transaction, exists, err := loadConfigTransaction(p)
	if err != nil || !exists {
		return err
	}
	configRaw, configExists, err := readConfig()
	if err != nil {
		return fmt.Errorf("读取 CLIProxyAPI 配置事务状态失败")
	}
	ownershipRaw, ownershipExists, err := readFileForTransaction(configOwnershipPath(p))
	if err != nil {
		return fmt.Errorf("读取 CLIProxyAPI 配置事务状态失败")
	}
	configBefore := transaction.ConfigBefore.matches(configRaw, configExists)
	configAfter := configMatchesAfter(transaction, configRaw, configExists)
	ownershipBefore := transaction.OwnershipBefore.matches(ownershipRaw, ownershipExists)
	ownershipAfter := transaction.OwnershipAfter.matches(ownershipRaw, ownershipExists)

	switch {
	case configAfter && ownershipAfter:
		return durableRemove(configTransactionPath(p))
	case configBefore && ownershipBefore:
		return durableRemove(configTransactionPath(p))
	case configAfter && ownershipBefore:
		// The config commit is known and complete. Finish the corresponding
		// ownership generation rather than rolling back a live configuration.
		if err := atomicWrite(configOwnershipPath(p), transaction.OwnershipAfter.Data, 0o600); err != nil {
			return fmt.Errorf("恢复 CLIProxyAPI 配置所有权失败")
		}
		if err := ensureConfigStillAfter(transaction, readConfig); err != nil {
			return err
		}
		return durableRemove(configTransactionPath(p))
	case configBefore && ownershipAfter:
		// Ownership advanced without its config generation. Restore only the
		// known previous ledger; an unknown config is never overwritten.
		if err := writeSnapshot(configOwnershipPath(p), transaction.OwnershipBefore); err != nil {
			return fmt.Errorf("恢复 CLIProxyAPI 配置所有权失败")
		}
		currentConfig, currentExists, err := readConfig()
		if err != nil || !transaction.ConfigBefore.matches(currentConfig, currentExists) {
			return fmt.Errorf("CLIProxyAPI 配置事务在恢复期间发生外部变更")
		}
		return durableRemove(configTransactionPath(p))
	default:
		return fmt.Errorf("CLIProxyAPI 配置事务存在未知外部变更；已停止自动恢复")
	}
}

func configMatchesAfter(transaction configTransaction, raw []byte, exists bool) bool {
	if !exists {
		return false
	}
	digest, err := yamlSemanticSHA256(raw)
	return err == nil && digest == transaction.ConfigAfterSemanticSHA256
}

func ensureConfigStillAfter(transaction configTransaction, readConfig configReadFunc) error {
	raw, exists, err := readConfig()
	if err != nil || !configMatchesAfter(transaction, raw, exists) {
		return fmt.Errorf("CLIProxyAPI 配置事务在恢复期间发生外部变更")
	}
	return nil
}

func writeSnapshot(path string, snapshot transactionFileSnapshot) error {
	if !snapshot.Exists {
		return durableRemove(path)
	}
	return atomicWrite(path, snapshot.Data, 0o600)
}

func commitConfigAndOwnership(p Paths, beforeConfig []byte, beforeConfigExists bool, afterConfig []byte, afterOwnership configOwnership, readConfig configReadFunc, writeConfig configWriteFunc) error {
	if err := recoverConfigTransaction(p, readConfig, writeConfig); err != nil {
		return err
	}
	transaction, err := beginConfigTransaction(p, beforeConfig, beforeConfigExists, afterConfig, afterOwnership)
	if err != nil {
		return err
	}
	afterOwnershipRaw := transaction.OwnershipAfter.Data

	currentConfig, currentConfigExists, err := readConfig()
	if err != nil || !transaction.ConfigBefore.matches(currentConfig, currentConfigExists) {
		return fmt.Errorf("CLIProxyAPI config.yaml 在提交前发生外部变更")
	}
	currentOwnership, currentOwnershipExists, err := readFileForTransaction(configOwnershipPath(p))
	if err != nil || !transaction.OwnershipBefore.matches(currentOwnership, currentOwnershipExists) {
		return fmt.Errorf("CLIProxyAPI 配置所有权在提交前发生外部变更")
	}
	if configMatchesAfter(transaction, currentConfig, currentConfigExists) && transaction.OwnershipAfter.matches(currentOwnership, currentOwnershipExists) {
		return nil
	}
	if err := saveConfigTransaction(p, transaction); err != nil {
		return err
	}
	if err := writeConfig(afterConfig, true); err != nil {
		return finishFailedConfigTransaction(p, transaction, readConfig, writeConfig, err)
	}
	if err := ensureConfigStillAfter(transaction, readConfig); err != nil {
		return finishFailedConfigTransaction(p, transaction, readConfig, writeConfig, err)
	}
	if err := atomicWrite(configOwnershipPath(p), afterOwnershipRaw, 0o600); err != nil {
		return finishFailedConfigTransaction(p, transaction, readConfig, writeConfig, err)
	}
	if err := ensureConfigStillAfter(transaction, readConfig); err != nil {
		return finishFailedConfigTransaction(p, transaction, readConfig, writeConfig, err)
	}
	ownershipRaw, ownershipExists, err := readFileForTransaction(configOwnershipPath(p))
	if err != nil || !transaction.OwnershipAfter.matches(ownershipRaw, ownershipExists) {
		return finishFailedConfigTransaction(p, transaction, readConfig, writeConfig, fmt.Errorf("CLIProxyAPI 配置所有权验证失败"))
	}
	return durableRemove(configTransactionPath(p))
}

func finishFailedConfigTransaction(p Paths, transaction configTransaction, readConfig configReadFunc, writeConfig configWriteFunc, original error) error {
	if err := recoverConfigTransaction(p, readConfig, writeConfig); err != nil {
		return fmt.Errorf("%w；配置事务保留待恢复", original)
	}
	configRaw, configExists, configErr := readConfig()
	ownershipRaw, ownershipExists, ownershipErr := readFileForTransaction(configOwnershipPath(p))
	if configErr == nil && ownershipErr == nil && configMatchesAfter(transaction, configRaw, configExists) && transaction.OwnershipAfter.matches(ownershipRaw, ownershipExists) {
		return nil
	}
	return original
}

func durableRemove(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func yamlSemanticSHA256(raw []byte) (string, error) {
	document, _, err := parseYAMLRoot(raw)
	if err != nil {
		return "", err
	}
	var canonical bytes.Buffer
	if err := writeCanonicalYAMLNode(&canonical, document); err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func writeCanonicalYAMLNode(writer io.Writer, node *yaml.Node) error {
	if node == nil {
		_, err := io.WriteString(writer, "nil;")
		return err
	}
	if _, err := io.WriteString(writer, strconv.Itoa(int(node.Kind))+":"+strconv.Quote(node.Tag)+":"+strconv.Quote(node.Value)+"["); err != nil {
		return err
	}
	switch node.Kind {
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return fmt.Errorf("CLIProxyAPI config.yaml 包含无效映射")
		}
		pairs := make([]string, 0, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			var pair bytes.Buffer
			if err := writeCanonicalYAMLNode(&pair, node.Content[i]); err != nil {
				return err
			}
			if err := writeCanonicalYAMLNode(&pair, node.Content[i+1]); err != nil {
				return err
			}
			pairs = append(pairs, pair.String())
		}
		sort.Strings(pairs)
		for _, pair := range pairs {
			if _, err := io.WriteString(writer, pair); err != nil {
				return err
			}
		}
	case yaml.AliasNode:
		if err := writeCanonicalYAMLNode(writer, node.Alias); err != nil {
			return err
		}
	default:
		for _, child := range node.Content {
			if err := writeCanonicalYAMLNode(writer, child); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(writer, "];")
	return err
}
