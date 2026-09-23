package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"grok_switch/internal/modelvariants"
)

// validLevelsBody is the exact shape CLIProxyAPI returns when it rejects an
// unsupported reasoning effort. The accepted list is per model, which is what
// makes one cheap request a complete answer.
func validLevelsBody(levels string) string {
	return `{"error":{"message":"level \"x\" not supported, valid levels: ` + levels + `","type":"invalid_request_error"}}`
}

// capabilityManager serves a catalog that starts with the supplied physical
// models and, after the config PUT, also advertises the generated aliases. That
// mirrors how CLIProxyAPI exposes a managed-alias fork, which the reconcile
// convergence check depends on.
//
// effortReply answers the tier probe (a deliberately invalid reasoning_effort).
// reachable decides the follow-up real request that proves an account can serve
// the model; probes counts both request kinds.
func capabilityManager(t *testing.T, physical []map[string]string, effortReply func(model string) (int, string), reachable func(model string) bool) (*Manager, func() int) {
	t.Helper()
	fixture := &configYAMLFixture{t: t, yaml: []byte("host: 127.0.0.1\nport: 8317\n")}
	probes := 0
	m, _ := testManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fixture.serve(w, r) {
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			data := append([]map[string]string(nil), physical...)
			if fixture.puts > 0 {
				for _, model := range physical {
					if _, isFast := modelvariants.TrustedCodexPhysicalFromFastLeaf(model["id"]); isFast {
						continue
					}
					if provider, ok := trustedCatalogProvider(model["owned_by"]); ok && provider == "codex" {
						data = append(data,
							map[string]string{"id": "subscription/codex/" + model["id"], "owned_by": model["owned_by"]},
						)
						if _, trusted := modelvariants.CodexFastAlias(model["id"]); trusted {
							data = append(data, map[string]string{"id": "subscription/codex/" + model["id"] + "-fast", "owned_by": model["owned_by"]})
						}
					}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/v1/chat/completions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			model, _ := body["model"].(string)
			probes++
			if body["reasoning_effort"] == capabilityProbeEffort {
				status, reply := effortReply(model)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(reply))
				return
			}
			// Real minimal request: the reachability half of the probe.
			if reachable != nil && reachable(model) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"model":"` + model + `","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"auth_unavailable: no auth available; last upstream error: model_not_found"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return m, func() int { return probes }
}

// alwaysReachable is the common case: an account can serve every model.
func alwaysReachable(string) bool { return true }

func accepted(model string) (int, string) {
	return http.StatusBadRequest, validLevelsBody("low, medium, high, xhigh, max")
}

// Regression test for the bug that the CLIProxyAPI 7.3.15 upgrade exposed:
// CLIProxyAPI validates reasoning_effort before resolving an account, so a model
// the subscription cannot access still answers with a full tier list. Granting
// capability from that answer alone advertises a Fast route that always fails.
// Capability must require a successful real request as well.
func TestTierListAloneDoesNotGrantCapabilityWhenUnreachable(t *testing.T) {
	modelvariants.ResetProbedCapabilities()
	t.Cleanup(modelvariants.ResetProbedCapabilities)

	m, probes := capabilityManager(t,
		[]map[string]string{{"id": "gpt-6-sol", "owned_by": "codex"}},
		accepted,                           // the proxy happily names the tiers
		func(string) bool { return false }, // but no account can serve it
	)
	if _, err := m.ReconcileModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probes() != 2 {
		t.Fatalf("probes = %d, want a tier probe plus a reachability request", probes())
	}
	if modelvariants.IsTrustedCodexPhysicalModel("gpt-6-sol") {
		t.Fatal("an unreachable model must not be granted Fast routing from the tier answer alone")
	}
	if _, ok := modelvariants.CodexFastAlias("gpt-6-sol"); ok {
		t.Fatal("an unreachable model must not get a Fast alias")
	}
	desired := generatedManagedConfig([]upstreamModel{{ID: "gpt-6-sol", OwnedBy: "codex"}})
	if len(desired.FastAliases) != 0 {
		t.Fatalf("generated Fast aliases for an unreachable model: %v", desired.FastAliases)
	}
	// It must stay retryable rather than being written off as permanently measured.
	if entry := mustLedger(t, m)["gpt-6-sol"]; !entry.Unavailable || entry.EffortsKnown {
		t.Fatalf("unreachable model must stay retryable: %+v", entry)
	}
}

// A stale Switch-owned capability record must be discarded and rebuilt rather
// than trusted or treated as a hard error.
func TestStaleCapabilityVersionIsDiscardedForReprobe(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "missing"), fakeKeys{})
	if err := m.Paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	stale := `{"version":1,"models":{"gpt-6-sol":{"fast":true,"efforts":["low"],"efforts_known":true}}}`
	if err := atomicWrite(capabilityLedgerPath(m.Paths), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger, err := loadCapabilityLedger(m.Paths)
	if err != nil {
		t.Fatalf("a stale Switch-owned record must not fail closed: %v", err)
	}
	if len(ledger.Models) != 0 {
		t.Fatalf("stale capability claims must be dropped: %+v", ledger.Models)
	}
	// A newer, unknown version is still rejected.
	if err := atomicWrite(capabilityLedgerPath(m.Paths), []byte(`{"version":99,"models":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCapabilityLedger(m.Paths); err == nil {
		t.Fatal("an unknown future version must still fail closed")
	}
}

func mustLedger(t *testing.T, m *Manager) map[string]modelCapability {
	t.Helper()
	ledger, err := loadCapabilityLedger(m.Paths)
	if err != nil {
		t.Fatal(err)
	}
	return ledger.Models
}

func TestParseAcceptedEfforts(t *testing.T) {
	cases := []struct {
		message string
		want    []string
		ok      bool
	}{
		{"level \"x\" not supported, valid levels: low, medium, high, xhigh, max", []string{"low", "medium", "high", "xhigh", "max"}, true},
		{"level \"x\" not supported, valid levels: low, medium, high, xhigh", []string{"low", "medium", "high", "xhigh"}, true},
		{"level \"x\" not supported, valid levels: low, low, high", []string{"low", "high"}, true},
		{"level \"x\" not supported, valid levels: low, none, high", []string{"low", "high"}, true},
		{"level \"x\" not supported, valid levels:", nil, false},
		{"model gpt-image-2 is only supported on /v1/images/generations", nil, false},
		{"", nil, false},
	}
	for _, tc := range cases {
		got, ok := parseAcceptedEfforts(tc.message)
		if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("parseAcceptedEfforts(%q) = %v/%v, want %v/%v", tc.message, got, ok, tc.want, tc.ok)
		}
	}
}

// A measured model must gain Fast routing and its exact tier list, and the
// request that measured it must never reach inference.
func TestReconcileProbesNewCodexModelAndGrantsCapabilities(t *testing.T) {
	modelvariants.ResetProbedCapabilities()
	t.Cleanup(modelvariants.ResetProbedCapabilities)

	m, probes := capabilityManager(t, []map[string]string{{"id": "gpt-6-sol", "owned_by": "codex"}}, accepted, alwaysReachable)
	models, err := m.ReconcileModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if probes() != 2 {
		t.Fatalf("probe requests = %d, want one tier probe plus one reachability request", probes())
	}
	if !modelvariants.IsTrustedCodexPhysicalModel("gpt-6-sol") {
		t.Fatal("a measured model must become eligible for a Fast route")
	}
	if got := modelvariants.TrustedCodexReasoningEffortsForPhysicalModel("gpt-6-sol"); !reflect.DeepEqual(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("measured tiers = %v", got)
	}
	ids := map[string]bool{}
	for _, model := range models {
		ids[model.ID] = true
	}
	if !ids["subscription/codex/gpt-6-sol"] {
		t.Fatalf("catalog missing the measured Standard alias: %v", ids)
	}
	// The capability grant must reach the generated config, which is what makes
	// the Fast alias exist and receive priority injection. The Fast route itself
	// is materialized on the subscription profile, which is covered by
	// TestProbedModelYieldsFastPairAndTiers in internal/server.
	desired := generatedManagedConfig([]upstreamModel{{ID: "gpt-6-sol", OwnedBy: "codex"}})
	if !reflect.DeepEqual(desired.FastAliases, []string{"subscription/codex/gpt-6-sol-fast"}) {
		t.Fatalf("generated fast aliases = %v", desired.FastAliases)
	}
}

// An unavailable model must stay unqualified and be retried, not remembered as
// permanently incapable.
func TestReconcileRetriesUnavailableModel(t *testing.T) {
	modelvariants.ResetProbedCapabilities()
	t.Cleanup(modelvariants.ResetProbedCapabilities)

	unavailable := func(string) (int, string) {
		return http.StatusServiceUnavailable, `{"error":{"message":"auth_unavailable: no auth available; last upstream error: model_not_found"}}`
	}
	m, probes := capabilityManager(t, []map[string]string{{"id": "gpt-6-sol", "owned_by": "codex"}}, unavailable, alwaysReachable)
	for round := 0; round < 2; round++ {
		if _, err := m.ReconcileModels(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if probes() != 2 {
		t.Fatalf("probe attempts = %d, want the unavailable model retried", probes())
	}
	if modelvariants.IsTrustedCodexPhysicalModel("gpt-6-sol") {
		t.Fatal("an unavailable model must not be granted Fast routing")
	}
}

// A model that will never serve chat completions is recorded as measured so it
// is not probed again on every reconcile.
func TestReconcileDoesNotReprobeMeasuredModel(t *testing.T) {
	modelvariants.ResetProbedCapabilities()
	t.Cleanup(modelvariants.ResetProbedCapabilities)

	reply := func(model string) (int, string) {
		if model == "gpt-image-9" {
			return http.StatusServiceUnavailable, `{"error":{"message":"model gpt-image-9 is only supported on /v1/images/generations"}}`
		}
		return accepted(model)
	}
	m, probes := capabilityManager(t, []map[string]string{
		{"id": "gpt-6-sol", "owned_by": "codex"},
		{"id": "gpt-image-9", "owned_by": "codex"},
	}, reply, alwaysReachable)

	if _, err := m.ReconcileModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	// gpt-6-sol needs a tier probe plus a reachability request; gpt-image-9 is
	// settled by its single non-transient endpoint-mismatch answer.
	first := probes()
	if first != 3 {
		t.Fatalf("first reconcile probes = %d, want 2 for the chat model and 1 for the image model", first)
	}
	if _, err := m.ReconcileModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probes() != first {
		t.Fatalf("probes grew to %d on a steady-state reconcile; measured models must not be re-probed", probes())
	}
	if modelvariants.IsTrustedCodexPhysicalModel("gpt-image-9") {
		t.Fatal("an endpoint-mismatch model must not gain Fast routing")
	}
}

// The capability record must survive a restart: startup publishes it before the
// ownership ledger is validated, because validating a probed Fast alias
// resolves it through the registry.
func TestCapabilityLedgerRoundTripAndStartupOrdering(t *testing.T) {
	modelvariants.ResetProbedCapabilities()
	t.Cleanup(modelvariants.ResetProbedCapabilities)

	dataDir := t.TempDir()
	m := NewManager(dataDir, t.TempDir(), filepath.Join(t.TempDir(), "missing"), fakeKeys{})
	if err := m.Paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	ledger := capabilityLedger{Models: map[string]modelCapability{
		"gpt-6-sol": {Fast: true, Efforts: []string{"low", "medium", "high", "xhigh", "max"}, EffortsKnown: true},
	}}
	if err := saveCapabilityLedger(m.Paths, ledger); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadCapabilityLedger(m.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Models["gpt-6-sol"].Fast || !reflect.DeepEqual(loaded.Models["gpt-6-sol"].Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("ledger round trip lost capability: %+v", loaded.Models["gpt-6-sol"])
	}

	// A Fast alias already owned on disk must keep validating even if the
	// capability record is gone, because the ownership ledger still claims it.
	modelvariants.SetProbedCapabilities([]string{"gpt-6-sol"}, map[string][]string{"gpt-6-sol": {"low"}})
	ownership := configOwnership{Version: configOwnershipVersion, Aliases: []ownedAliasIdentity{
		{Channel: "codex", Name: "gpt-6-sol", Alias: "subscription/codex/gpt-6-sol"},
		{Channel: "codex", Name: "gpt-6-sol", Alias: "subscription/codex/gpt-6-sol-fast"},
	}}
	raw, err := marshalConfigOwnership(ownership)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(configOwnershipPath(m.Paths), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	modelvariants.ResetProbedCapabilities()
	publishCapabilities(capabilityLedger{})
	if _, _, err := loadConfigOwnership(m.Paths); err != nil {
		t.Fatalf("an owned Fast alias must still validate against its ownership record: %v", err)
	}
	if _, err := loadCapabilityLedger(m.Paths); err != nil {
		t.Fatalf("capability record must remain readable: %v", err)
	}
}

func TestCapabilityLedgerRejectsUnfamiliarVersion(t *testing.T) {
	m := NewManager(t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "missing"), fakeKeys{})
	if err := m.Paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(capabilityLedgerPath(m.Paths), []byte(`{"version":99,"models":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCapabilityLedger(m.Paths); err == nil {
		t.Fatal("an unfamiliar capability record must fail closed")
	}
}

// An unparsable tier answer must leave the model unqualified rather than
// granting Fast on a message we did not understand.
func TestUnparsableTierAnswerDoesNotGrantFast(t *testing.T) {
	modelvariants.ResetProbedCapabilities()
	t.Cleanup(modelvariants.ResetProbedCapabilities)

	m, _ := capabilityManager(t, []map[string]string{{"id": "gpt-7-sol", "owned_by": "codex"}}, func(string) (int, string) {
		return http.StatusBadRequest, `{"error":{"message":"invalid reasoning configuration"}}`
	}, alwaysReachable)
	if _, err := m.ReconcileModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	ledger, err := loadCapabilityLedger(m.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if entry := ledger.Models["gpt-7-sol"]; entry.Fast {
		t.Fatalf("unparsable tier answer must not grant Fast: %+v", entry)
	}
	if modelvariants.IsTrustedCodexPhysicalModel("gpt-7-sol") {
		t.Fatal("unparsable tier answer must not grant a Fast route")
	}
}

func TestTransientUnavailability(t *testing.T) {
	for body, want := range map[string]bool{
		`{"error":{"message":"auth_unavailable: no auth available (providers=codex)"}}`:                 true,
		"{\"error\":{\"message\":\"last upstream error: model_not_found: The model `gpt-6-sol` ...\"}}": true,
		`{"error":{"message":"model gpt-image-2 is only supported on /v1/images"}}`:                     false,
		`{"error":{"message":"boom"}}`: false,
	} {
		if got := transientUnavailability(body); got != want {
			t.Fatalf("transientUnavailability(%s) = %v, want %v", body, got, want)
		}
	}
}

// Probing must never displace the static registry entries.
func TestProbeOverlayNeverRemovesStaticCapabilities(t *testing.T) {
	modelvariants.ResetProbedCapabilities()
	t.Cleanup(modelvariants.ResetProbedCapabilities)

	modelvariants.SetProbedCapabilities([]string{"gpt-6-sol"}, map[string][]string{"gpt-6-sol": {"low"}})
	for _, id := range []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna"} {
		if !modelvariants.IsTrustedCodexPhysicalModel(id) {
			t.Fatalf("static model %q lost trust", id)
		}
	}
	if got := modelvariants.TrustedCodexReasoningEffortsForPhysicalModel("gpt-5.6-sol"); len(got) != 5 {
		t.Fatalf("static tiers = %v", got)
	}
	// gpt-6-astra declares tiers without a Fast route and must keep them.
	if _, ok := modelvariants.CodexFastAlias("gpt-6-astra"); ok {
		t.Fatal("gpt-6-astra must not gain a Fast route")
	}
	if got := modelvariants.TrustedCodexReasoningEffortsForPhysicalModel("gpt-6-astra"); len(got) != 5 {
		t.Fatalf("gpt-6-astra tiers = %v", got)
	}
}
