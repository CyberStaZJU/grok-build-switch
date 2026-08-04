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
	"strings"

	"grok_switch/internal/modelvariants"

	"gopkg.in/yaml.v3"
)

const (
	configOwnershipVersion = 1
	aliasOwnershipMarker   = "grok-build-switch:subscription-alias:v1"
	fastOwnershipMarker    = "grok-build-switch:fast-service-tier:v1"
)

type ownedAliasIdentity struct {
	Channel string `json:"channel"`
	Name    string `json:"name"`
	Alias   string `json:"alias"`
}

type configOwnership struct {
	Version             int                  `json:"version"`
	Aliases             []ownedAliasIdentity `json:"aliases,omitempty"`
	FastRuleFingerprint string               `json:"fast_rule_fingerprint,omitempty"`
}

type managedConfig struct {
	Aliases     oauthModelAliases
	FastAliases []string
}

type managedBaseConfig struct {
	Host             string
	Port             int
	ManagementSecret string
	AuthDir          string
	InferenceKey     string
	ProxyURL         string
}

func configOwnershipPath(p Paths) string {
	return filepath.Join(p.Root, "config-ownership.json")
}

func loadConfigOwnership(p Paths) (configOwnership, bool, error) {
	raw, err := os.ReadFile(configOwnershipPath(p))
	if errors.Is(err, os.ErrNotExist) {
		return configOwnership{}, false, nil
	}
	if err != nil {
		return configOwnership{}, false, err
	}
	ownership, err := decodeConfigOwnership(raw)
	if err != nil {
		return configOwnership{}, true, err
	}
	return ownership, true, nil
}

func decodeConfigOwnership(raw []byte) (configOwnership, error) {
	var ownership configOwnership
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ownership); err != nil {
		return configOwnership{}, fmt.Errorf("CLIProxyAPI 配置所有权记录无效")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return configOwnership{}, fmt.Errorf("CLIProxyAPI 配置所有权记录无效")
	}
	if err := validateConfigOwnership(ownership); err != nil {
		return configOwnership{}, err
	}
	return ownership, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON document")
		}
		return err
	}
	return nil
}

func marshalConfigOwnership(ownership configOwnership) ([]byte, error) {
	if err := validateConfigOwnership(ownership); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(ownership, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func saveConfigOwnership(p Paths, ownership configOwnership) error {
	raw, err := marshalConfigOwnership(ownership)
	if err != nil {
		return err
	}
	return atomicWrite(configOwnershipPath(p), raw, 0o600)
}

func validateConfigOwnership(ownership configOwnership) error {
	if ownership.Version != configOwnershipVersion {
		return fmt.Errorf("CLIProxyAPI 配置所有权记录版本不受支持")
	}
	seen := map[string]bool{}
	for _, identity := range ownership.Aliases {
		if !validOwnedAliasIdentity(identity) {
			return fmt.Errorf("CLIProxyAPI 配置所有权记录包含无效别名")
		}
		key := ownedAliasKey(identity)
		if seen[key] {
			return fmt.Errorf("CLIProxyAPI 配置所有权记录包含重复别名")
		}
		seen[key] = true
	}
	if ownership.FastRuleFingerprint != "" {
		fingerprint := ownership.FastRuleFingerprint
		decoded, err := hex.DecodeString(fingerprint)
		if err != nil || len(decoded) != sha256.Size || fingerprint != strings.ToLower(fingerprint) {
			return fmt.Errorf("CLIProxyAPI 配置所有权记录包含无效 Fast 规则")
		}
	}
	return nil
}

func validOwnedAliasIdentity(identity ownedAliasIdentity) bool {
	if identity.Channel != strings.TrimSpace(identity.Channel) || identity.Name != strings.TrimSpace(identity.Name) || identity.Alias != strings.TrimSpace(identity.Alias) {
		return false
	}
	if identity.Channel == "" || identity.Name == "" || identity.Alias == "" || strings.ContainsAny(identity.Name, "/\r\n") {
		return false
	}
	provider, ok := providerForAliasChannel(identity.Channel)
	if !ok {
		return false
	}
	standard := "subscription/" + provider + "/" + identity.Name
	if identity.Alias == standard {
		return true
	}
	if identity.Channel != "codex" {
		return false
	}
	physicalID, ok := modelvariants.TrustedCodexPhysicalFromFastAlias(identity.Alias)
	return ok && physicalID == identity.Name
}

func providerForAliasChannel(channel string) (string, bool) {
	switch channel {
	case "codex":
		return "codex", true
	case "antigravity":
		return "gemini", true
	case "xai":
		return "grok", true
	default:
		return "", false
	}
}

func ownedAliasKey(identity ownedAliasIdentity) string {
	// Ownership ledgers only accept canonical channel names. Keep the channel
	// exact here so a non-canonical YAML key such as "Codex" can never inherit
	// ownership from the canonical "codex" ledger entry.
	return identity.Channel + "\x00" + identity.Name + "\x00" + identity.Alias
}

func aliasIdentity(channel string, alias oauthModelAlias) ownedAliasIdentity {
	return ownedAliasIdentity{Channel: channel, Name: strings.TrimSpace(alias.Name), Alias: strings.TrimSpace(alias.Alias)}
}

// previousConfigOwnership returns the explicit ledger when present. Only on the
// first migration does it inspect the legacy sidecar, and even then it claims
// entries having the exact shape emitted by historical Switch versions.
type configOwnershipState struct {
	Ownership     configOwnership
	LegacyAliases oauthModelAliases
	LegacyExists  bool
}

// previousConfigOwnershipState returns the explicit ledger when present. Only
// the first migration reads the legacy sidecar, and it decodes that sidecar
// exactly once so ownership derivation and optional YAML seeding use the same
// snapshot. Existing but unreadable or unfamiliar migration state fails closed.
func previousConfigOwnershipState(p Paths) (configOwnershipState, error) {
	ownership, exists, err := loadConfigOwnership(p)
	if err != nil {
		return configOwnershipState{}, err
	}
	if exists {
		return configOwnershipState{Ownership: ownership}, nil
	}
	aliases, legacyExists, err := loadLegacyAliases(p)
	if err != nil {
		return configOwnershipState{}, err
	}
	return configOwnershipState{
		Ownership: configOwnership{
			Version: configOwnershipVersion,
			Aliases: legacyOwnedAliases(aliases),
		},
		LegacyAliases: aliases,
		LegacyExists:  legacyExists,
	}, nil
}

func previousConfigOwnership(p Paths) (configOwnership, error) {
	state, err := previousConfigOwnershipState(p)
	return state.Ownership, err
}

func legacyOwnedAliases(aliases oauthModelAliases) []ownedAliasIdentity {
	owned := make([]ownedAliasIdentity, 0)
	for channel, entries := range aliases {
		provider, ok := providerForAliasChannel(channel)
		if !ok {
			continue
		}
		for _, entry := range entries {
			name := strings.TrimSpace(entry.Name)
			if name == "" || name != entry.Name || strings.ContainsAny(name, "/\r\n") || !entry.Fork || entry.ForceMapping {
				continue
			}
			if entry.DisplayName != "" && entry.DisplayName != name {
				continue
			}
			if entry.Alias != "subscription/"+provider+"/"+name {
				continue
			}
			owned = append(owned, ownedAliasIdentity{Channel: channel, Name: name, Alias: entry.Alias})
		}
	}
	sortOwnedAliases(owned)
	return dedupeOwnedAliases(owned)
}

func loadLegacyAliases(p Paths) (oauthModelAliases, bool, error) {
	raw, err := os.ReadFile(modelAliasesPath(p))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("读取旧 CLIProxyAPI 别名记录失败")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true, fmt.Errorf("旧 CLIProxyAPI 别名记录格式无效")
	}
	var aliases oauthModelAliases
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&aliases); err != nil {
		return nil, true, fmt.Errorf("旧 CLIProxyAPI 别名记录格式无效")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, true, fmt.Errorf("旧 CLIProxyAPI 别名记录格式无效")
	}
	if aliases == nil {
		aliases = oauthModelAliases{}
	}
	return aliases, true, nil
}

func sortOwnedAliases(aliases []ownedAliasIdentity) {
	sort.Slice(aliases, func(i, j int) bool {
		if aliases[i].Channel != aliases[j].Channel {
			return aliases[i].Channel < aliases[j].Channel
		}
		if aliases[i].Alias != aliases[j].Alias {
			return aliases[i].Alias < aliases[j].Alias
		}
		return aliases[i].Name < aliases[j].Name
	})
}

func dedupeOwnedAliases(aliases []ownedAliasIdentity) []ownedAliasIdentity {
	out := aliases[:0]
	seen := map[string]bool{}
	for _, identity := range aliases {
		key := ownedAliasKey(identity)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, identity)
	}
	return out
}

func legacyAliasSeed(aliases oauthModelAliases) ([]byte, error) {
	if len(aliases) == 0 {
		return nil, nil
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	aliasMap := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content, stringNode("oauth-model-alias"), aliasMap)
	channels := make([]string, 0, len(aliases))
	for channel := range aliases {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	seenTargets := map[string]bool{}
	for _, channel := range channels {
		if strings.TrimSpace(channel) == "" || strings.TrimSpace(channel) != channel {
			return nil, fmt.Errorf("旧 CLIProxyAPI 别名记录包含无效 channel")
		}
		sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, entry := range aliases[channel] {
			if strings.TrimSpace(entry.Name) == "" || strings.TrimSpace(entry.Alias) == "" || strings.ContainsAny(entry.Name+entry.Alias, "\x00\r\n") {
				return nil, fmt.Errorf("旧 CLIProxyAPI 别名记录包含无效别名")
			}
			aliasKey := strings.ToLower(strings.TrimSpace(entry.Alias))
			if seenTargets[aliasKey] {
				return nil, fmt.Errorf("旧 CLIProxyAPI 别名记录包含重复别名")
			}
			seenTargets[aliasKey] = true
			entry.Name = strings.TrimSpace(entry.Name)
			entry.Alias = strings.TrimSpace(entry.Alias)
			sequence.Content = append(sequence.Content, unmarkedAliasYAMLNode(entry))
		}
		if len(sequence.Content) > 0 {
			aliasMap.Content = append(aliasMap.Content, stringNode(channel), sequence)
		}
	}
	if len(aliasMap.Content) == 0 {
		return nil, nil
	}
	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	return encodeYAML(document)
}

func managedConfigFromOwnership(ownership configOwnership) managedConfig {
	desired := managedConfig{Aliases: oauthModelAliases{}}
	seen := map[string]bool{}
	for _, identity := range ownership.Aliases {
		if !validOwnedAliasIdentity(identity) {
			continue
		}
		key := ownedAliasKey(identity)
		if seen[key] {
			continue
		}
		seen[key] = true
		desired.Aliases[identity.Channel] = append(desired.Aliases[identity.Channel], oauthModelAlias{
			Name: identity.Name, Alias: identity.Alias, Fork: true, DisplayName: identity.Name,
		})
	}
	// A legacy sidecar contains only Standard aliases. Upgrade an exact trusted
	// Standard anchor to its Fast sibling while preserving the same physical ID.
	for _, identity := range ownership.Aliases {
		if identity.Channel != "codex" {
			continue
		}
		physicalID, ok := modelvariants.TrustedCodexPhysicalFromStandardAlias(identity.Alias)
		if !ok || physicalID != identity.Name {
			continue
		}
		fast, _ := modelvariants.CodexFastAlias(physicalID)
		fastIdentity := ownedAliasIdentity{Channel: "codex", Name: physicalID, Alias: fast}
		if seen[ownedAliasKey(fastIdentity)] {
			continue
		}
		seen[ownedAliasKey(fastIdentity)] = true
		desired.Aliases["codex"] = append(desired.Aliases["codex"], oauthModelAlias{
			Name: physicalID, Alias: fast, Fork: true, DisplayName: physicalID,
		})
	}
	for channel := range desired.Aliases {
		sort.Slice(desired.Aliases[channel], func(i, j int) bool {
			return desired.Aliases[channel][i].Alias < desired.Aliases[channel][j].Alias
		})
		for _, entry := range desired.Aliases[channel] {
			if _, ok := modelvariants.TrustedCodexPhysicalFromFastAlias(entry.Alias); channel == "codex" && ok {
				desired.FastAliases = append(desired.FastAliases, entry.Alias)
			}
		}
	}
	desired.FastAliases = sortedUnique(desired.FastAliases)
	return desired
}

func mergeManagedConfig(raw []byte, desired managedConfig, previous configOwnership, base *managedBaseConfig) ([]byte, configOwnership, error) {
	if err := validateManagedConfig(desired); err != nil {
		return nil, configOwnership{}, err
	}
	if previous.Version == 0 {
		previous.Version = configOwnershipVersion
	}
	if err := validateConfigOwnership(previous); err != nil {
		return nil, configOwnership{}, err
	}
	document, root, err := parseYAMLRoot(raw)
	if err != nil {
		return nil, configOwnership{}, err
	}
	if base != nil {
		if err := mergeManagedBase(root, *base); err != nil {
			return nil, configOwnership{}, err
		}
	}
	if err := mergeManagedAliases(root, desired.Aliases, previous.Aliases); err != nil {
		return nil, configOwnership{}, err
	}
	if err := mergeManagedFastRule(root, desired.FastAliases, previous.FastRuleFingerprint); err != nil {
		return nil, configOwnership{}, err
	}
	ownership := ownershipForManagedConfig(desired)
	encoded, err := encodeYAML(document)
	if err != nil {
		return nil, configOwnership{}, err
	}
	if err := verifyManagedConfig(encoded, desired); err != nil {
		return nil, configOwnership{}, err
	}
	return encoded, ownership, nil
}

func validateManagedConfig(desired managedConfig) error {
	seenAliases := map[string]bool{}
	identities := map[string]bool{}
	for channel, entries := range desired.Aliases {
		if strings.TrimSpace(channel) == "" || strings.TrimSpace(channel) != channel {
			return fmt.Errorf("Switch 生成了无效订阅别名 channel")
		}
		for _, entry := range entries {
			identity := aliasIdentity(channel, entry)
			if identity.Name == "" || identity.Alias == "" || strings.ContainsAny(identity.Name, "\r\n") {
				return fmt.Errorf("Switch 生成了无效订阅别名")
			}
			key := ownedAliasKey(identity)
			if identities[key] {
				return fmt.Errorf("Switch 生成了重复订阅别名")
			}
			identities[key] = true
			aliasKey := strings.ToLower(identity.Alias)
			if seenAliases[aliasKey] {
				return fmt.Errorf("Switch 生成了冲突订阅别名")
			}
			seenAliases[aliasKey] = true
		}
	}
	for _, fastAlias := range sortedUnique(desired.FastAliases) {
		physicalID, ok := modelvariants.TrustedCodexPhysicalFromFastAlias(fastAlias)
		if !ok {
			return fmt.Errorf("Fast 路由不在可信模型注册表中")
		}
		fastIdentity := ownedAliasIdentity{Channel: "codex", Name: physicalID, Alias: fastAlias}
		standard, _ := modelvariants.CodexStandardAlias(physicalID)
		standardIdentity := ownedAliasIdentity{Channel: "codex", Name: physicalID, Alias: standard}
		if !identities[ownedAliasKey(fastIdentity)] || !identities[ownedAliasKey(standardIdentity)] {
			return fmt.Errorf("Fast 路由缺少同一物理模型的 Standard 锚点")
		}
	}
	if len(sortedUnique(desired.FastAliases)) != len(desired.FastAliases) {
		return fmt.Errorf("Switch 生成了重复 Fast 路由")
	}
	return nil
}

func ownershipForManagedConfig(desired managedConfig) configOwnership {
	ownership := configOwnership{Version: configOwnershipVersion}
	for channel, entries := range desired.Aliases {
		for _, entry := range entries {
			identity := aliasIdentity(channel, entry)
			if validOwnedAliasIdentity(identity) {
				ownership.Aliases = append(ownership.Aliases, identity)
			}
		}
	}
	sortOwnedAliases(ownership.Aliases)
	ownership.Aliases = dedupeOwnedAliases(ownership.Aliases)
	if len(desired.FastAliases) > 0 {
		ownership.FastRuleFingerprint = fastRuleFingerprint(desired.FastAliases)
	}
	return ownership
}

func parseYAMLRoot(raw []byte) (*yaml.Node, *yaml.Node, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
		return document, root, nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, fmt.Errorf("CLIProxyAPI config.yaml 格式无效")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("CLIProxyAPI config.yaml 只能包含一个 YAML 文档")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("CLIProxyAPI config.yaml 根节点必须是映射")
	}
	if err := rejectDuplicateYAMLKeys(document.Content[0], "root"); err != nil {
		return nil, nil, err
	}
	return &document, document.Content[0], nil
}

func rejectDuplicateYAMLKeys(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content)%2 != 0 {
			return fmt.Errorf("CLIProxyAPI config.yaml 包含无效映射")
		}
		seen := map[string]bool{}
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode {
				return fmt.Errorf("CLIProxyAPI config.yaml 包含非标量键")
			}
			identity := key.Tag + "\x00" + key.Value
			if seen[identity] {
				return fmt.Errorf("CLIProxyAPI config.yaml 包含重复键 %q", key.Value)
			}
			seen[identity] = true
			if err := rejectDuplicateYAMLKeys(node.Content[i+1], path+"."+key.Value); err != nil {
				return err
			}
		}
		return nil
	}
	for _, child := range node.Content {
		if err := rejectDuplicateYAMLKeys(child, path); err != nil {
			return err
		}
	}
	return nil
}

func encodeYAML(document *yaml.Node) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("CLIProxyAPI config.yaml 编码失败")
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("CLIProxyAPI config.yaml 编码失败")
	}
	return buffer.Bytes(), nil
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, int, bool) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, -1, false
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Kind == yaml.ScalarNode && mapping.Content[i].Value == key {
			return mapping.Content[i+1], i, true
		}
	}
	return nil, -1, false
}

func ensureMappingValue(mapping *yaml.Node, key string) (*yaml.Node, error) {
	if value, _, ok := mappingValue(mapping, key); ok {
		if value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("CLIProxyAPI config.yaml 的 %s 必须是映射", key)
		}
		return value, nil
	}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, stringNode(key), value)
	return value, nil
}

func removeMappingPair(mapping *yaml.Node, keyIndex int) {
	mapping.Content = append(mapping.Content[:keyIndex], mapping.Content[keyIndex+2:]...)
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func intNode(value int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", value)}
}

func boolNode(value bool) *yaml.Node {
	if value {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}
}

func setMappingNode(mapping *yaml.Node, key string, desired *yaml.Node) {
	if current, _, ok := mappingValue(mapping, key); ok {
		desired.HeadComment = current.HeadComment
		desired.LineComment = current.LineComment
		desired.FootComment = current.FootComment
		*current = *desired
		return
	}
	mapping.Content = append(mapping.Content, stringNode(key), desired)
}

func mergeManagedBase(root *yaml.Node, base managedBaseConfig) error {
	if strings.TrimSpace(base.Host) == "" || base.Port <= 0 || base.ManagementSecret == "" || base.AuthDir == "" || base.InferenceKey == "" {
		return fmt.Errorf("CLIProxyAPI 基础配置不完整")
	}
	setMappingNode(root, "host", stringNode(base.Host))
	setMappingNode(root, "port", intNode(base.Port))
	remote, err := ensureMappingValue(root, "remote-management")
	if err != nil {
		return err
	}
	setMappingNode(remote, "allow-remote", boolNode(false))
	setMappingNode(remote, "secret-key", stringNode(base.ManagementSecret))
	setMappingNode(remote, "disable-control-panel", boolNode(true))
	setMappingNode(root, "auth-dir", stringNode(base.AuthDir))
	apiKeys := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{stringNode(base.InferenceKey)}}
	setMappingNode(root, "api-keys", apiKeys)
	setMappingNode(root, "proxy-url", stringNode(base.ProxyURL))
	setMappingNode(root, "debug", boolNode(false))
	setMappingNode(root, "commercial-mode", boolNode(true))
	setMappingNode(root, "logging-to-file", boolNode(false))
	setMappingNode(root, "usage-statistics-enabled", boolNode(false))
	return nil
}

var canonicalAliasChannels = map[string]bool{
	"vertex":      true,
	"aistudio":    true,
	"antigravity": true,
	"claude":      true,
	"codex":       true,
	"kimi":        true,
	"xai":         true,
}

func validateExistingAliasChannels(aliasMap *yaml.Node) error {
	seenFolded := map[string]string{}
	for i := 0; i+1 < len(aliasMap.Content); i += 2 {
		channelNode := aliasMap.Content[i]
		sequence := aliasMap.Content[i+1]
		if channelNode.Kind != yaml.ScalarNode || sequence.Kind != yaml.SequenceNode {
			return fmt.Errorf("CLIProxyAPI config.yaml 包含无效 oauth-model-alias channel")
		}
		channel := channelNode.Value
		folded := strings.ToLower(channel)
		if previous, exists := seenFolded[folded]; exists {
			return fmt.Errorf("CLIProxyAPI config.yaml 包含大小写等价的 oauth-model-alias channel %q 和 %q", previous, channel)
		}
		seenFolded[folded] = channel
		if canonicalAliasChannels[folded] && channel != folded {
			return fmt.Errorf("CLIProxyAPI config.yaml 包含非规范 oauth-model-alias channel %q", channel)
		}
	}
	return nil
}

func mergeManagedAliases(root *yaml.Node, desired oauthModelAliases, previous []ownedAliasIdentity) error {
	aliasMap, aliasKeyIndex, exists := mappingValue(root, "oauth-model-alias")
	if exists && aliasMap.Kind != yaml.MappingNode {
		return fmt.Errorf("CLIProxyAPI config.yaml 的 oauth-model-alias 必须是映射")
	}
	if exists {
		if err := validateExistingAliasChannels(aliasMap); err != nil {
			return err
		}
	}
	if !exists {
		if len(desired) == 0 {
			return nil
		}
		aliasMap = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, stringNode("oauth-model-alias"), aliasMap)
		aliasKeyIndex = len(root.Content) - 2
	}

	previousKeys := map[string]bool{}
	for _, identity := range previous {
		previousKeys[ownedAliasKey(identity)] = true
	}
	preservedAliasTargets := map[string]bool{}
	consumedChannels := map[string]bool{}
	newContent := make([]*yaml.Node, 0, len(aliasMap.Content))
	for i := 0; i+1 < len(aliasMap.Content); i += 2 {
		channelNode := aliasMap.Content[i]
		sequence := aliasMap.Content[i+1]
		if channelNode.Kind != yaml.ScalarNode || sequence.Kind != yaml.SequenceNode {
			return fmt.Errorf("CLIProxyAPI config.yaml 包含无效 oauth-model-alias channel")
		}
		channel := channelNode.Value
		if consumedChannels[channel] {
			return fmt.Errorf("CLIProxyAPI config.yaml 包含重复 oauth-model-alias channel %q", channel)
		}
		remove, err := aliasRemovalIndices(channel, sequence, previousKeys)
		if err != nil {
			return err
		}
		kept := make([]*yaml.Node, 0, len(sequence.Content)+len(desired[channel]))
		for index, entry := range sequence.Content {
			if remove[index] {
				continue
			}
			kept = append(kept, entry)
			if alias, ok := aliasTargetFromNode(entry); ok {
				aliasKey := strings.ToLower(alias)
				if preservedAliasTargets[aliasKey] {
					return fmt.Errorf("CLIProxyAPI config.yaml 包含重复别名 %q", alias)
				}
				preservedAliasTargets[aliasKey] = true
			}
		}
		consumedChannels[channel] = true
		if len(kept) == 0 && len(desired[channel]) == 0 {
			continue
		}
		sequence.Content = kept
		newContent = append(newContent, channelNode, sequence)
	}
	channels := make([]string, 0, len(desired))
	for channel := range desired {
		if !consumedChannels[channel] && len(desired[channel]) > 0 {
			channels = append(channels, channel)
		}
	}
	sort.Strings(channels)
	for _, channel := range channels {
		newContent = append(newContent, stringNode(channel), &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	}
	aliasMap.Content = newContent
	for channel, entries := range desired {
		sequence, _, ok := mappingValue(aliasMap, channel)
		if !ok || sequence.Kind != yaml.SequenceNode {
			return fmt.Errorf("CLIProxyAPI config.yaml 无法写入订阅别名 channel")
		}
		for _, entry := range entries {
			aliasKey := strings.ToLower(entry.Alias)
			if preservedAliasTargets[aliasKey] {
				return fmt.Errorf("订阅别名 %q 已由用户配置占用", entry.Alias)
			}
			preservedAliasTargets[aliasKey] = true
			sequence.Content = append(sequence.Content, aliasYAMLNode(entry))
		}
	}
	if len(aliasMap.Content) == 0 {
		removeMappingPair(root, aliasKeyIndex)
	}
	return nil
}

func aliasRemovalIndices(channel string, sequence *yaml.Node, previous map[string]bool) (map[int]bool, error) {
	remove := map[int]bool{}
	markedByIdentity := map[string]int{}
	lastFallback := map[string]int{}
	for index, entry := range sequence.Content {
		identity, hasIdentity := aliasIdentityFromNode(channel, entry)
		var marked, authenticated bool
		if hasIdentity {
			marked, authenticated = hasAliasOwnershipMarker(entry, ownedAliasFromIdentity(identity))
		} else {
			marked = hasAnyMarkerPrefix(entry, aliasOwnershipMarker)
		}
		if marked {
			if !hasIdentity {
				return nil, fmt.Errorf("CLIProxyAPI config.yaml 包含无法验证的 Switch 别名所有权标记")
			}
			key := ownedAliasKey(identity)
			if !previous[key] || !exactAliasNode(entry, ownedAliasFromIdentity(identity)) || !authenticated {
				return nil, fmt.Errorf("CLIProxyAPI config.yaml 包含与所有权记录不一致的 Switch 别名标记")
			}
			if _, duplicate := markedByIdentity[key]; duplicate {
				return nil, fmt.Errorf("CLIProxyAPI config.yaml 包含重复的 Switch 别名标记")
			}
			markedByIdentity[key] = index
			remove[index] = true
			continue
		}
		if hasIdentity {
			key := ownedAliasKey(identity)
			if previous[key] && exactAliasNode(entry, ownedAliasFromIdentity(identity)) {
				lastFallback[key] = index
			}
		}
	}
	for key, index := range lastFallback {
		if _, marked := markedByIdentity[key]; !marked {
			// CLIProxyAPI may remove comments while preserving exact semantics. The
			// historical Switch entry was appended last, so remove only the last
			// exact Switch-shaped ledger match.
			remove[index] = true
		}
	}
	return remove, nil
}

func ownedAliasFromIdentity(identity ownedAliasIdentity) oauthModelAlias {
	return oauthModelAlias{
		Name:        identity.Name,
		Alias:       identity.Alias,
		Fork:        true,
		DisplayName: identity.Name,
	}
}

func aliasIdentityFromNode(channel string, node *yaml.Node) (ownedAliasIdentity, bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return ownedAliasIdentity{}, false
	}
	name, nameOK := scalarMappingString(node, "name")
	alias, aliasOK := scalarMappingString(node, "alias")
	if !nameOK || !aliasOK || strings.TrimSpace(name) == "" || strings.TrimSpace(alias) == "" {
		return ownedAliasIdentity{}, false
	}
	return ownedAliasIdentity{Channel: channel, Name: strings.TrimSpace(name), Alias: strings.TrimSpace(alias)}, true
}

func aliasTargetFromNode(node *yaml.Node) (string, bool) {
	alias, ok := scalarMappingString(node, "alias")
	alias = strings.TrimSpace(alias)
	return alias, ok && alias != ""
}

func scalarMappingString(mapping *yaml.Node, key string) (string, bool) {
	value, _, ok := mappingValue(mapping, key)
	if !ok || value.Kind != yaml.ScalarNode {
		return "", false
	}
	return value.Value, true
}

func aliasYAMLNode(alias oauthModelAlias) *yaml.Node {
	node := unmarkedAliasYAMLNode(alias)
	node.HeadComment = aliasOwnershipMarker + ":" + aliasOwnershipFingerprint(alias)
	return node
}

func aliasOwnershipFingerprint(alias oauthModelAlias) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, aliasOwnershipMarker+"\n")
	_, _ = io.WriteString(hash, alias.Name+"\n"+alias.Alias+"\n")
	return hex.EncodeToString(hash.Sum(nil))
}

func hasAliasOwnershipMarker(node *yaml.Node, alias oauthModelAlias) (bool, bool) {
	legacy := hasOwnershipMarker(node, aliasOwnershipMarker)
	current := hasOwnershipMarker(node, aliasOwnershipMarker+":"+aliasOwnershipFingerprint(alias))
	return legacy || current || hasAnyMarkerPrefix(node, aliasOwnershipMarker), current
}

func unmarkedAliasYAMLNode(alias oauthModelAlias) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	node.Content = append(node.Content,
		stringNode("name"), stringNode(alias.Name),
		stringNode("alias"), stringNode(alias.Alias),
		stringNode("fork"), boolNode(alias.Fork),
	)
	if alias.DisplayName != "" {
		node.Content = append(node.Content, stringNode("display-name"), stringNode(alias.DisplayName))
	}
	if alias.ForceMapping {
		node.Content = append(node.Content, stringNode("force-mapping"), boolNode(true))
	}
	return node
}

func exactAliasNode(node *yaml.Node, desired oauthModelAlias) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	allowed := map[string]bool{"name": true, "alias": true, "fork": true, "display-name": true, "force-mapping": true}
	seen := map[string]bool{}
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || !allowed[key.Value] || seen[key.Value] {
			return false
		}
		seen[key.Value] = true
	}
	name, nameOK := scalarMappingString(node, "name")
	alias, aliasOK := scalarMappingString(node, "alias")
	fork, forkOK := scalarMappingBool(node, "fork")
	if !nameOK || !aliasOK || !forkOK || name != desired.Name || alias != desired.Alias || fork != desired.Fork {
		return false
	}
	display, hasDisplay := scalarMappingString(node, "display-name")
	if desired.DisplayName == "" {
		if hasDisplay && display != "" {
			return false
		}
	} else if !hasDisplay || display != desired.DisplayName {
		return false
	}
	force, hasForce := scalarMappingBool(node, "force-mapping")
	if desired.ForceMapping {
		return hasForce && force
	}
	return !hasForce || !force
}

func scalarMappingBool(mapping *yaml.Node, key string) (bool, bool) {
	value, _, ok := mappingValue(mapping, key)
	if !ok || value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
		return false, false
	}
	switch strings.ToLower(value.Value) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func mergeManagedFastRule(root *yaml.Node, fastAliases []string, previousFingerprint string) error {
	payload, payloadKeyIndex, exists := mappingValue(root, "payload")
	if exists && payload.Kind != yaml.MappingNode {
		return fmt.Errorf("CLIProxyAPI config.yaml 的 payload 必须是映射")
	}
	if !exists {
		if len(fastAliases) == 0 {
			return nil
		}
		payload = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, stringNode("payload"), payload)
		payloadKeyIndex = len(root.Content) - 2
	}
	override, overrideKeyIndex, overrideExists := mappingValue(payload, "override")
	if overrideExists && override.Kind != yaml.SequenceNode {
		return fmt.Errorf("CLIProxyAPI config.yaml 的 payload.override 必须是序列")
	}
	if !overrideExists {
		override = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		payload.Content = append(payload.Content, stringNode("override"), override)
		overrideKeyIndex = len(payload.Content) - 2
	}

	remove := map[int]bool{}
	markedIndex := -1
	lastFingerprintMatch := -1
	for index, rule := range override.Content {
		fingerprint, exact := exactFastRuleFingerprint(rule)
		marked, authenticated := hasFastOwnershipMarker(rule, fingerprint)
		if marked {
			if previousFingerprint == "" || !exact || fingerprint != previousFingerprint || !authenticated {
				return fmt.Errorf("CLIProxyAPI config.yaml 包含与所有权记录不一致的 Switch Fast 规则标记")
			}
			if markedIndex >= 0 {
				return fmt.Errorf("CLIProxyAPI config.yaml 包含重复的 Switch Fast 规则标记")
			}
			markedIndex = index
			remove[index] = true
			continue
		}
		if previousFingerprint != "" && exact && fingerprint == previousFingerprint {
			lastFingerprintMatch = index
		}
	}
	if markedIndex < 0 && lastFingerprintMatch >= 0 {
		// The managed rule is always appended, so the last exact fingerprint is
		// the safest fallback when an external formatter removed comments.
		remove[lastFingerprintMatch] = true
	}
	kept := make([]*yaml.Node, 0, len(override.Content)+1)
	for index, rule := range override.Content {
		if !remove[index] {
			kept = append(kept, rule)
		}
	}
	if len(fastAliases) > 0 {
		kept = append(kept, fastRuleYAMLNode(fastAliases))
	}
	override.Content = kept
	if len(override.Content) == 0 {
		removeMappingPair(payload, overrideKeyIndex)
	}
	if len(payload.Content) == 0 {
		removeMappingPair(root, payloadKeyIndex)
	}
	return nil
}

func fastRuleYAMLNode(fastAliases []string) *yaml.Node {
	fingerprint := fastRuleFingerprint(fastAliases)
	rule := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: fastOwnershipMarker + ":" + fingerprint}
	models := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, alias := range sortedUnique(fastAliases) {
		model := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		model.Content = append(model.Content,
			stringNode("name"), stringNode(alias),
			stringNode("protocol"), stringNode("codex"),
		)
		models.Content = append(models.Content, model)
	}
	params := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	params.Content = append(params.Content, stringNode("service_tier"), stringNode("priority"))
	rule.Content = append(rule.Content, stringNode("models"), models, stringNode("params"), params)
	return rule
}

func exactFastRuleFingerprint(rule *yaml.Node) (string, bool) {
	if rule == nil || rule.Kind != yaml.MappingNode || len(rule.Content) != 4 {
		return "", false
	}
	models, _, hasModels := mappingValue(rule, "models")
	params, _, hasParams := mappingValue(rule, "params")
	if !hasModels || !hasParams || models.Kind != yaml.SequenceNode || params.Kind != yaml.MappingNode || len(params.Content) != 2 {
		return "", false
	}
	serviceTier, _, ok := mappingValue(params, "service_tier")
	if !ok || serviceTier.Kind != yaml.ScalarNode || serviceTier.Value != "priority" || len(models.Content) == 0 {
		return "", false
	}
	aliases := make([]string, 0, len(models.Content))
	seen := map[string]bool{}
	for _, model := range models.Content {
		if model.Kind != yaml.MappingNode || len(model.Content) != 4 {
			return "", false
		}
		name, nameOK := scalarMappingString(model, "name")
		protocol, protocolOK := scalarMappingString(model, "protocol")
		if !nameOK || !protocolOK || protocol != "codex" {
			return "", false
		}
		if _, trusted := modelvariants.TrustedCodexPhysicalFromFastAlias(name); !trusted || seen[name] {
			return "", false
		}
		seen[name] = true
		aliases = append(aliases, name)
	}
	return fastRuleFingerprint(aliases), true
}

func hasFastOwnershipMarker(node *yaml.Node, fingerprint string) (bool, bool) {
	legacy := hasOwnershipMarker(node, fastOwnershipMarker)
	current := fingerprint != "" && hasOwnershipMarker(node, fastOwnershipMarker+":"+fingerprint)
	return legacy || current || hasAnyMarkerPrefix(node, fastOwnershipMarker), current
}

func fastRuleFingerprint(fastAliases []string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, fastOwnershipMarker+"\n")
	for _, alias := range sortedUnique(fastAliases) {
		_, _ = io.WriteString(hash, alias+"\n")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func hasOwnershipMarker(node *yaml.Node, marker string) bool {
	return nodeCommentMatches(node, func(comment string) bool { return commentHasMarker(comment, marker) })
}

func hasAnyMarkerPrefix(node *yaml.Node, marker string) bool {
	return nodeCommentMatches(node, func(comment string) bool {
		for _, line := range strings.Split(comment, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
			if line == marker || strings.HasPrefix(line, marker+":") {
				return true
			}
		}
		return false
	})
}

func nodeCommentMatches(node *yaml.Node, matches func(string) bool) bool {
	if node == nil {
		return false
	}
	if matches(node.HeadComment) || matches(node.LineComment) || matches(node.FootComment) {
		return true
	}
	// yaml.v3 may attach a sequence-item comment to the first key instead of the
	// mapping node after a decode/encode cycle.
	if node.Kind == yaml.MappingNode && len(node.Content) > 0 {
		first := node.Content[0]
		return matches(first.HeadComment) || matches(first.LineComment) || matches(first.FootComment)
	}
	return false
}

func commentHasMarker(comment, marker string) bool {
	for _, line := range strings.Split(comment, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if line == marker {
			return true
		}
	}
	return false
}

func verifyManagedConfig(raw []byte, desired managedConfig) error {
	_, root, err := parseYAMLRoot(raw)
	if err != nil {
		return err
	}
	aliasMap, _, hasAliases := mappingValue(root, "oauth-model-alias")
	for channel, entries := range desired.Aliases {
		if !hasAliases || aliasMap.Kind != yaml.MappingNode {
			return fmt.Errorf("CLIProxyAPI 未保留 Switch 订阅别名")
		}
		sequence, _, ok := mappingValue(aliasMap, channel)
		if !ok || sequence.Kind != yaml.SequenceNode {
			return fmt.Errorf("CLIProxyAPI 未保留 Switch 订阅别名")
		}
		for _, desiredAlias := range entries {
			matches := 0
			for _, entry := range sequence.Content {
				identity, ok := aliasIdentityFromNode(channel, entry)
				if !ok || ownedAliasKey(identity) != ownedAliasKey(aliasIdentity(channel, desiredAlias)) {
					continue
				}
				if !exactAliasNode(entry, desiredAlias) {
					return fmt.Errorf("CLIProxyAPI 改写了 Switch 订阅别名语义")
				}
				matches++
			}
			if matches != 1 {
				return fmt.Errorf("CLIProxyAPI 未精确保留 Switch 订阅别名")
			}
		}
	}
	payload, _, hasPayload := mappingValue(root, "payload")
	wantFingerprint := ""
	if len(desired.FastAliases) > 0 {
		wantFingerprint = fastRuleFingerprint(desired.FastAliases)
	}
	matchingFastRules := 0
	markedFastRules := 0
	if hasPayload && payload.Kind == yaml.MappingNode {
		if override, _, ok := mappingValue(payload, "override"); ok && override.Kind == yaml.SequenceNode {
			for _, rule := range override.Content {
				fingerprint, exact := exactFastRuleFingerprint(rule)
				if marked, _ := hasFastOwnershipMarker(rule, fingerprint); marked {
					markedFastRules++
				}
				if exact && wantFingerprint != "" && fingerprint == wantFingerprint {
					matchingFastRules++
				}
			}
		}
	}
	if len(desired.FastAliases) > 0 && (matchingFastRules != 1 || markedFastRules > 1) {
		return fmt.Errorf("CLIProxyAPI 未精确保留 Switch Fast 规则")
	}
	if len(desired.FastAliases) == 0 && markedFastRules != 0 {
		return fmt.Errorf("CLIProxyAPI 保留了过期 Switch Fast 规则")
	}
	return nil
}
