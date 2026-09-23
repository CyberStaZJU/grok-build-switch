package cliproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"grok_switch/internal/modelvariants"
)

// Capability discovery for Codex subscription models.
//
// Switch decides locally which models may carry a Fast route and which
// reasoning tiers to offer. Rather than inferring that from a model name, the
// manager asks the local CLIProxyAPI what it actually accepts.
//
// One request per model yields both answers. CLIProxyAPI validates
// reasoning_effort itself, before contacting upstream, and rejects an
// unsupported level with HTTP 400 naming the levels the model does accept:
//
//	level "x" not supported, valid levels: low, medium, high, xhigh, max
//
// That single 400 therefore proves the proxy resolved the model, has an
// account for it, and that the listed tiers are the model's real set. A 503
// (no auth available / model_not_found) or any other status leaves the model
// unqualified. The request is rejected before inference, so discovery costs no
// completion tokens.
//
// Limitation, deliberately recorded here: this proves the proxy accepts and can
// resolve the model. It does not prove that injecting service_tier: priority
// makes the upstream route faster. The proxy echoes service_tier: "default" for
// both Standard and Fast aliases, including the known-good -fast routes, so no
// response field can confirm priority is honored. Fast is therefore enabled on
// proxy acceptance, which is the strongest signal available; a subsequent
// upstream failure still surfaces to the caller as a real error.

const (
	capabilityLedgerVersion = 1
	capabilityLedgerName    = "capability.json"
	// maxCapabilityProbesPerRun bounds how many models one reconcile probes.
	maxCapabilityProbesPerRun = 24
	capabilityProbeTimeout    = 20 * time.Second
	// capabilityDiscoveryBudget bounds the total time a single reconcile spends
	// measuring. Discovery runs inside the caller's request and config lock, so
	// a slow or unreachable proxy must not stall saving the model selection.
	// Models left unprobed are simply retried on the next reconcile.
	capabilityDiscoveryBudget = 45 * time.Second
	// capabilityProbeEffort is never a valid tier anywhere, which is what makes
	// the proxy answer with its accepted list instead of running inference.
	capabilityProbeEffort = "__switch_capability_probe__"
)

type modelCapability struct {
	// Fast records that the proxy accepted the model, which is what licenses a
	// generated -fast alias and the priority injection for it.
	Fast bool `json:"fast"`
	// Efforts is the exact accepted tier list, most useful first.
	Efforts []string `json:"efforts,omitempty"`
	// EffortsKnown distinguishes "no tiers" from "not measured yet".
	EffortsKnown bool `json:"efforts_known"`
	// Unavailable records a transient or account-level failure so the next
	// reconcile retries instead of treating the model as permanently incapable.
	Unavailable bool   `json:"unavailable,omitempty"`
	ProbedAt    string `json:"probed_at,omitempty"`
}

type capabilityLedger struct {
	Version int                        `json:"version"`
	Models  map[string]modelCapability `json:"models,omitempty"`
}

func capabilityLedgerPath(p Paths) string {
	return filepath.Join(p.Root, capabilityLedgerName)
}

// loadCapabilityLedger returns the recorded capabilities. A missing ledger is
// an empty one; an unreadable or unfamiliar ledger is an error so callers can
// fail closed instead of silently regenerating routes from nothing.
func loadCapabilityLedger(p Paths) (capabilityLedger, error) {
	ledger := capabilityLedger{Version: capabilityLedgerVersion, Models: map[string]modelCapability{}}
	raw, err := os.ReadFile(capabilityLedgerPath(p))
	if errors.Is(err, os.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return capabilityLedger{}, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return ledger, nil
	}
	var decoded capabilityLedger
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return capabilityLedger{}, fmt.Errorf("能力记录无法解析")
	}
	if decoded.Version != capabilityLedgerVersion {
		return capabilityLedger{}, fmt.Errorf("能力记录版本不受支持")
	}
	if decoded.Models == nil {
		decoded.Models = map[string]modelCapability{}
	}
	for id, entry := range decoded.Models {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
			return capabilityLedger{}, fmt.Errorf("能力记录包含无效模型")
		}
		decoded.Models[id] = modelCapability{
			Fast:         entry.Fast,
			Efforts:      normalizedEfforts(entry.Efforts),
			EffortsKnown: entry.EffortsKnown,
			Unavailable:  entry.Unavailable,
			ProbedAt:     strings.TrimSpace(entry.ProbedAt),
		}
	}
	return decoded, nil
}

func saveCapabilityLedger(p Paths, ledger capabilityLedger) error {
	if ledger.Version == 0 {
		ledger.Version = capabilityLedgerVersion
	}
	if ledger.Models == nil {
		ledger.Models = map[string]modelCapability{}
	}
	raw, err := json.Marshal(ledger)
	if err != nil {
		return fmt.Errorf("保存能力记录失败")
	}
	return atomicWrite(capabilityLedgerPath(p), raw, 0o600)
}

// publishCapabilities computes the measured overlay and hands it to the model
// registry. It must run before any code validates or regenerates routes.
//
// Fast routes come from two records, both written by Switch: the capability
// ledger, and the ownership ledger. The ownership ledger is authoritative
// because it is what route validation resolves against, so a lost
// capability.json degrades to "Fast route without reasoning tiers" instead of a
// startup failure on an alias the ownership record still claims.
func publishCapabilities(p Paths, ledger capabilityLedger) error {
	fast := make([]string, 0, len(ledger.Models))
	efforts := map[string][]string{}
	for id, entry := range ledger.Models {
		if entry.Fast {
			fast = append(fast, id)
		}
		if entry.EffortsKnown {
			efforts[id] = entry.Efforts
		}
	}
	owned, err := ownedFastCodexModels(p)
	if err != nil {
		return err
	}
	fast = append(fast, owned...)
	modelvariants.SetProbedCapabilities(fast, efforts)
	return nil
}

// ownedFastCodexModels recovers Fast models Switch already owns from the
// ownership ledger. This reads the ledger without validating it, because
// validation is what needs the overlay in the first place; an unreadable ledger
// is reported by the validated load path that runs next.
func ownedFastCodexModels(p Paths) ([]string, error) {
	raw, err := os.ReadFile(configOwnershipPath(p))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lenient struct {
		Aliases []ownedAliasIdentity `json:"aliases"`
	}
	if json.Unmarshal(raw, &lenient) != nil {
		return nil, nil
	}
	out := []string{}
	for _, identity := range lenient.Aliases {
		if identity.Channel != "codex" || strings.TrimSpace(identity.Name) == "" {
			continue
		}
		if identity.Alias == "subscription/codex/"+identity.Name+"-fast" {
			out = append(out, identity.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// normalizedEfforts trims, drops blanks and duplicates, and preserves the
// proxy's own ordering while dropping the local disable sentinel.
func normalizedEfforts(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, effort := range in {
		effort = strings.TrimSpace(effort)
		if effort == "" || effort == "none" || seen[effort] {
			continue
		}
		seen[effort] = true
		out = append(out, effort)
	}
	return out
}

// parseAcceptedEfforts extracts the accepted tier list from a proxy validation
// error. CLIProxyAPI names the levels it accepts after "valid levels:".
func parseAcceptedEfforts(message string) ([]string, bool) {
	const marker = "valid levels:"
	idx := strings.Index(message, marker)
	if idx < 0 {
		return nil, false
	}
	rest := message[idx+len(marker):]
	rest = strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
	efforts := normalizedEfforts(strings.Split(rest, ","))
	if len(efforts) == 0 {
		return nil, false
	}
	return efforts, true
}

// inferenceProbe sends one capability probe through the local proxy and
// reports the HTTP status with the raw body, because the interesting answer
// arrives as a rejected request rather than a successful one.
func (m *Manager) inferenceProbe(ctx context.Context, model string) (int, []byte, error) {
	body, err := json.Marshal(map[string]any{
		"model":            model,
		"messages":         []map[string]string{{"role": "user", "content": "capability probe"}},
		"max_tokens":       1,
		"stream":           false,
		"reasoning_effort": capabilityProbeEffort,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("能力探测请求编码失败")
	}
	return m.requestRawStatus(ctx, false, "POST", "/v1/chat/completions", "application/json", body, 1<<18)
}

// probeModelCapability classifies one model from a single probe request.
func (m *Manager) probeModelCapability(ctx context.Context, model string) modelCapability {
	entry := modelCapability{ProbedAt: time.Now().UTC().Format(time.RFC3339)}
	probeCtx, cancel := context.WithTimeout(ctx, capabilityProbeTimeout)
	defer cancel()
	status, raw, err := m.inferenceProbe(probeCtx, model)
	if err != nil {
		entry.Unavailable = true
		return entry
	}
	switch {
	case status == 400:
		var payload struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &payload) != nil {
			return entry
		}
		efforts, ok := parseAcceptedEfforts(payload.Error.Message)
		if !ok {
			return entry
		}
		entry.Fast = true
		entry.Efforts = efforts
		entry.EffortsKnown = true
		return entry
	case status == 503 && transientUnavailability(string(raw)):
		// No account can serve the model right now. Leave it unqualified so the
		// next reconcile retries instead of recording a permanent verdict.
		entry.Unavailable = true
		return entry
	default:
		// Any other answer is definitive for this model's chat capability: a
		// success, a schema rejection, or a message naming the endpoints the
		// model actually serves (image models). Record it as measured with no
		// accepted tiers so it is not probed again.
		entry.EffortsKnown = true
		return entry
	}
}

// transientUnavailability reports whether a 503 body describes a model that may
// become available later, as opposed to one that will never serve chat
// completions. The proxy forwards upstream account errors verbatim, so a
// missing model or absent auth is retryable while an endpoint mismatch is not.
func transientUnavailability(body string) bool {
	return strings.Contains(body, "model_not_found") || strings.Contains(body, "no auth available")
}

// discoverCapabilities probes catalog models whose capability is new or whose
// tiers were never measured. Models already measured are left untouched, so a
// steady-state reconcile issues no probe requests.
func (m *Manager) discoverCapabilities(ctx context.Context, models []upstreamModel, ledger capabilityLedger) (capabilityLedger, bool) {
	if ledger.Models == nil {
		ledger.Models = map[string]modelCapability{}
	}
	candidates := capabilityProbeCandidates(models, ledger)
	if len(candidates) > maxCapabilityProbesPerRun {
		candidates = candidates[:maxCapabilityProbesPerRun]
	}
	deadline := time.Now().Add(capabilityDiscoveryBudget)
	changed := false
	for _, id := range candidates {
		if time.Now().After(deadline) {
			break
		}
		entry := m.probeModelCapability(ctx, id)
		ledger.Models[id] = entry
		changed = true
	}
	return ledger, changed
}

// capabilityProbeCandidates lists Codex models worth probing: newly seen ones,
// and those whose accepted tiers are still unknown so a model that was
// temporarily unavailable is retried on a later reconcile.
func capabilityProbeCandidates(models []upstreamModel, ledger capabilityLedger) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" || strings.HasPrefix(id, "subscription/") || seen[id] {
			continue
		}
		provider, trusted := trustedCatalogProvider(model.OwnedBy)
		if !trusted || provider != "codex" {
			continue
		}
		if _, isFastLeaf := modelvariants.TrustedCodexPhysicalFromFastLeaf(id); isFastLeaf || isRecursiveTrustedCodexFastLeaf(id) {
			continue
		}
		if entry, ok := ledger.Models[id]; ok && entry.EffortsKnown {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
