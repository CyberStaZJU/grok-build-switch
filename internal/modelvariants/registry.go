// Package modelvariants contains the Switch-owned registry for logical
// Standard/Fast subscription routes. Capability comes from two sources: the
// static trusted registry below, and a measured capability overlay populated
// from the cliproxy probe ledger (see internal/cliproxy/capability.go). Nothing
// is inferred from suffixes, labels, or broad model-name patterns.
package modelvariants

import (
	"strings"
	"sync"
)

const (
	SpeedTierStandard = "standard"
	SpeedTierFast     = "fast"
)

// staticTrustedCodexPhysicalModels are the models Switch qualifies without a
// probe. The measured overlay extends this set at runtime.
var staticTrustedCodexPhysicalModels = map[string]struct{}{
	"gpt-5.6-terra": {},
	"gpt-5.6-sol":   {},
	"gpt-5.6-luna":  {},
}

var staticTrustedCodexReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// staticReasoningOnlyCodexModels declare reasoning tiers without a Fast route.
// gpt-6-astra is the historical case: Switch declares its tiers, and it must
// keep them even when a probe has not run yet or cannot reach the model.
var staticReasoningOnlyCodexModels = map[string][]string{
	"gpt-6-astra": {"low", "medium", "high", "xhigh", "max"},
}

var (
	capabilityMu          sync.RWMutex
	probedFastCodexModels = map[string]struct{}{}
	probedCodexEfforts    = map[string][]string{}
)

// SetProbedCapabilities replaces the measured capability overlay. It is called
// with the cliproxy probe ledger, which records what the local proxy actually
// served. A model whose effort entry is present but empty suppresses the static
// default, so an unmeasured tier list is never advertised.
func SetProbedCapabilities(fastModels []string, efforts map[string][]string) {
	capabilityMu.Lock()
	defer capabilityMu.Unlock()
	next := make(map[string]struct{}, len(fastModels))
	for _, id := range fastModels {
		if id = strings.TrimSpace(id); id != "" {
			next[id] = struct{}{}
		}
	}
	probedFastCodexModels = next
	nextEfforts := make(map[string][]string, len(efforts))
	for id, list := range efforts {
		if id = strings.TrimSpace(id); id != "" {
			nextEfforts[id] = append([]string(nil), list...)
		}
	}
	probedCodexEfforts = nextEfforts
}

// ResetProbedCapabilities clears the overlay. Tests use it to restore the
// package to its static-only state.
func ResetProbedCapabilities() {
	capabilityMu.Lock()
	defer capabilityMu.Unlock()
	probedFastCodexModels = map[string]struct{}{}
	probedCodexEfforts = map[string][]string{}
}

func probedFastCodexPhysicalModel(id string) bool {
	capabilityMu.RLock()
	defer capabilityMu.RUnlock()
	_, ok := probedFastCodexModels[id]
	return ok
}

func probedCodexReasoningEfforts(id string) ([]string, bool) {
	capabilityMu.RLock()
	defer capabilityMu.RUnlock()
	list, ok := probedCodexEfforts[id]
	return append([]string(nil), list...), ok
}

func staticTrustedCodexPhysicalModel(id string) bool {
	_, ok := staticTrustedCodexPhysicalModels[strings.TrimSpace(id)]
	return ok
}

// codexPhysicalModelIDs lists every model that may carry a Fast route: the
// static registry plus models the probe measured as Fast-capable.
func codexPhysicalModelIDs() []string {
	capabilityMu.RLock()
	ids := make([]string, 0, len(staticTrustedCodexPhysicalModels)+len(probedFastCodexModels))
	for id := range staticTrustedCodexPhysicalModels {
		ids = append(ids, id)
	}
	for id := range probedFastCodexModels {
		ids = append(ids, id)
	}
	capabilityMu.RUnlock()
	return ids
}

// IsTrustedCodexPhysicalModel reports whether a model may be presented as a
// Fast-capable Codex route, from the static registry or the measured overlay.
func IsTrustedCodexPhysicalModel(id string) bool {
	return staticTrustedCodexPhysicalModel(id) || probedFastCodexPhysicalModel(strings.TrimSpace(id))
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
	for _, physicalID := range codexPhysicalModelIDs() {
		standard, _ := CodexStandardAlias(physicalID)
		if alias == standard {
			return physicalID, true
		}
	}
	return "", false
}

func TrustedCodexPhysicalFromFastAlias(alias string) (string, bool) {
	alias = strings.TrimSpace(alias)
	for _, physicalID := range codexPhysicalModelIDs() {
		fast, _ := CodexFastAlias(physicalID)
		if alias == fast {
			return physicalID, true
		}
	}
	return "", false
}

func TrustedCodexPhysicalFromFastLeaf(leaf string) (string, bool) {
	leaf = strings.TrimSpace(leaf)
	for _, physicalID := range codexPhysicalModelIDs() {
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

// TrustedCodexReasoningPhysicalFromAlias resolves reasoning capability without granting Fast routing.
func TrustedCodexReasoningPhysicalFromAlias(alias string) (string, bool) {
	leaf := strings.TrimSpace(alias)
	if idx := strings.LastIndex(leaf, "/"); idx >= 0 {
		leaf = leaf[idx+1:]
	}
	if _, ok := staticReasoningOnlyCodexModels[leaf]; ok && strings.HasPrefix(strings.TrimSpace(alias), "subscription/codex/") {
		return leaf, true
	}
	if physicalID, ok := TrustedCodexPhysicalFromStandardAlias(alias); ok {
		return physicalID, true
	}
	return TrustedCodexPhysicalFromFastAlias(alias)
}

// TrustedCodexReasoningEffortsForPhysicalModel reports the measured tier list
// when the probe recorded one, the static list for statically declared models,
// and an empty list for a model whose tiers were never measured.
func TrustedCodexReasoningEffortsForPhysicalModel(physicalID string) []string {
	physicalID = strings.TrimSpace(physicalID)
	if list, ok := probedCodexReasoningEfforts(physicalID); ok {
		return list
	}
	if list, ok := staticReasoningOnlyCodexModels[physicalID]; ok {
		return append([]string(nil), list...)
	}
	if staticTrustedCodexPhysicalModel(physicalID) {
		return append([]string(nil), staticTrustedCodexReasoningEfforts...)
	}
	return nil
}

// TrustedCodexReasoningEfforts returns the canonical static tier list. It is a
// compatibility helper, not a per-model lookup: use
// TrustedCodexReasoningEffortsForPhysicalModel for a specific model.
func TrustedCodexReasoningEfforts() []string {
	return append([]string(nil), staticTrustedCodexReasoningEfforts...)
}
