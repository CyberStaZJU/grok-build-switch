// Package modelvariants contains the exact, Switch-owned registry for logical
// Standard/Fast subscription routes. It deliberately does not infer capability
// from suffixes, labels, or broad model-name patterns.
package modelvariants

import "strings"

const (
	SpeedTierStandard = "standard"
	SpeedTierFast     = "fast"
)

var trustedCodexPhysicalModels = map[string]struct{}{
	"gpt-5.6-terra": {},
	"gpt-5.6-sol":   {},
	"gpt-5.6-luna":  {},
}

var trustedCodexReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max"}

func IsTrustedCodexPhysicalModel(id string) bool {
	_, ok := trustedCodexPhysicalModels[strings.TrimSpace(id)]
	return ok
}

func CodexStandardAlias(physicalID string) (string, bool) {
	physicalID = strings.TrimSpace(physicalID)
	if !IsTrustedCodexPhysicalModel(physicalID) {
		return "", false
	}
	return "subscription/codex/" + physicalID, true
}

func CodexFastAlias(physicalID string) (string, bool) {
	standard, ok := CodexStandardAlias(physicalID)
	if !ok {
		return "", false
	}
	return standard + "-fast", true
}

func TrustedCodexPhysicalFromStandardAlias(alias string) (string, bool) {
	alias = strings.TrimSpace(alias)
	for physicalID := range trustedCodexPhysicalModels {
		standard, _ := CodexStandardAlias(physicalID)
		if alias == standard {
			return physicalID, true
		}
	}
	return "", false
}

func TrustedCodexPhysicalFromFastAlias(alias string) (string, bool) {
	alias = strings.TrimSpace(alias)
	for physicalID := range trustedCodexPhysicalModels {
		fast, _ := CodexFastAlias(physicalID)
		if alias == fast {
			return physicalID, true
		}
	}
	return "", false
}

func TrustedCodexPhysicalFromFastLeaf(leaf string) (string, bool) {
	leaf = strings.TrimSpace(leaf)
	for physicalID := range trustedCodexPhysicalModels {
		fast, _ := TrustedCodexFastLeaf(physicalID)
		if leaf == fast {
			return physicalID, true
		}
	}
	return "", false
}

func TrustedCodexFastLeaf(physicalID string) (string, bool) {
	physicalID = strings.TrimSpace(physicalID)
	if !IsTrustedCodexPhysicalModel(physicalID) {
		return "", false
	}
	return physicalID + "-fast", true
}

func TrustedCodexReasoningEfforts() []string {
	return append([]string(nil), trustedCodexReasoningEfforts...)
}
