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
// Two requests per model are needed, because each answer alone is misleading.
//
// The tier list comes from a deliberately invalid reasoning_effort. CLIProxyAPI
// validates the level itself and rejects it with HTTP 400 naming the levels the
// model accepts:
//
//	level "x" not supported, valid levels: low, medium, high, xhigh, max
//
// That validation happens before account resolution, so a 400 proves the model
// is a known chat model and yields its real tier set, but it does NOT prove an
// account can serve it. A model the subscription cannot access still returns
// 400 with a full tier list.
//
// Reachability therefore needs the second, model-derived probe: send a real
// minimal request and require HTTP 200. Requiring both means a model is only
// granted Fast and tiers when the proxy both knows it and can actually route
// it. Neither request spends meaningful tokens (max_tokens 1, and the rejected
// one never reaches inference).
//
// Limitation, deliberately recorded here: this proves the proxy accepts and can
// serve the model. It does not prove that injecting service_tier: priority
// makes the upstream route faster. The proxy echoes service_tier: "default" for
// both Standard and Fast aliases, including the known-good -fast routes, so no
// response field can confirm priority is honored. Fast is therefore enabled on
// proxy acceptance, which is the strongest signal available; a subsequent
// upstream failure still surfaces to the caller as a real error.

const (
	// capabilityLedgerVersion 2 records only tiers confirmed reachable. Version 1
	// granted capability from the tier answer alone, which CLIProxyAPI returns
	// even for models the subscription cannot access, so it is discarded.
	capabilityLedgerVersion = 2
	capabilityLedgerName    = "capability.json"
	// maxCapabilityProbesPerRun bounds how many models one reconcile probes.
	maxCapabilityProbesPerRun = 24
	capabilityProbeTimeout    = 20 * time.Second
	// capabilityDiscoveryBudget bounds the total time a single reconcile spends
	// measuring. Discovery runs inside the caller's request and config lock, so
	// a slow or unreachable proxy must not stall saving the model selection.
	// Models left unprobed are simply retried on the next reconcile.
	capabilityDiscoveryBudget = 60 * time.Second
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
// an empty one. An unreadable, malformed, or foreign ledger is an error so
// callers fail closed instead of silently regenerating routes from nothing. An
// older Switch-owned version is discarded and rebuilt by re-probing, because a
// stale capability record is worse than re-measuring.
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
		if decoded.Version > 0 && decoded.Version < capabilityLedgerVersion {
			// Reads must never write; the next reconcile persists the rebuild.
			return ledger, nil
		}
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

// publishCapabilities hands the measured overlay to the model registry. It must
// run before any code validates or regenerates routes, because the registry is
// what decides which models may carry a generated Fast route.
//
// The ledger is the only source. Trust is deliberately not recovered from the
// ownership ledger: a recorded Fast alias whose model is no longer measured as
// Fast-capable must be able to disappear, and re-trusting it from ownership
// would keep a withdrawn route alive. A lost capability record therefore costs a
// re-probe on the next reconcile, which restores the measured set.
func publishCapabilities(ledger capabilityLedger) {
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
	modelvariants.SetProbedCapabilities(fast, efforts)
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

// probeModelCapability classifies one model from two minimal requests: one that
// elicits the accepted tier list, and one that proves an account can serve it.
// Both are required, because the tier answer alone is returned for models the
// subscription cannot reach.
func (m *Manager) probeModelCapability(ctx context.Context, model string) modelCapability {
	entry := modelCapability{ProbedAt: time.Now().UTC().Format(time.RFC3339)}
	probeCtx, cancel := context.WithTimeout(ctx, capabilityProbeTimeout)
	defer cancel()

	status, raw, err := m.inferenceProbe(probeCtx, model)
	if err != nil {
		entry.Unavailable = true
		return entry
	}
	if status == 503 && transientUnavailability(string(raw)) {
		// No account can serve the model right now. Leave it unqualified so the
		// next reconcile retries instead of recording a permanent verdict.
		entry.Unavailable = true
		return entry
	}
	if status != 400 {
		// A success or any other answer is definitive for this model's chat
		// capability: record it as measured with no tier list so it is not
		// probed again.
		entry.EffortsKnown = true
		return entry
	}
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
		// A 400 that does not name accepted levels is not a tier answer, for
		// example an endpoint mismatch. Treat it as measured with no tiers.
		entry.EffortsKnown = true
		return entry
	}

	// The tier list is known, but validation runs before account resolution, so
	// confirm an account can actually serve the model before granting anything.
	reachable, retryable := m.modelReachable(probeCtx, model)
	if !reachable {
		// An unreachable model must not be remembered as permanently measured:
		// a missing account is usually temporary, so retry on the next
		// reconcile. A permanent-looking failure is recorded to stop re-probing.
		entry.Unavailable = retryable
		entry.EffortsKnown = !retryable
		return entry
	}
	entry.Fast = true
	entry.Efforts = efforts
	entry.EffortsKnown = true
	return entry
}

// modelReachable reports whether a real minimal request to the model succeeds.
// retryable is true when the failure looks like temporary account or upstream
// unavailability rather than a permanent property of the model.
func (m *Manager) modelReachable(ctx context.Context, model string) (reachable, retryable bool) {
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ok"}},
		"max_tokens": 1,
		"stream":     false,
	})
	if err != nil {
		return false, true
	}
	status, raw, err := m.requestRawStatus(ctx, false, "POST", "/v1/chat/completions", "application/json", body, 1<<18)
	if err != nil {
		return false, true
	}
	if status == 200 {
		return true, false
	}
	// A missing model or absent account is retryable: subscription access can
	// change. Anything else is treated as a permanent property of the model.
	return false, transientUnavailability(string(raw)) || status >= 500 || status == 429
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
