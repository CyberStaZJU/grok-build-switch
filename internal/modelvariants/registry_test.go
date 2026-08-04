package modelvariants

import (
	"reflect"
	"testing"
)

func TestTrustedCodexRegistryUsesExactPhysicalAllowlist(t *testing.T) {
	for _, physicalID := range []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-luna"} {
		if !IsTrustedCodexPhysicalModel(physicalID) {
			t.Fatalf("trusted physical model %q was rejected", physicalID)
		}
		standard, ok := CodexStandardAlias(physicalID)
		if !ok || standard != "subscription/codex/"+physicalID {
			t.Fatalf("standard alias for %q = %q, %v", physicalID, standard, ok)
		}
		fast, ok := CodexFastAlias(physicalID)
		if !ok || fast != standard+"-fast" {
			t.Fatalf("fast alias for %q = %q, %v", physicalID, fast, ok)
		}
		leaf, ok := TrustedCodexFastLeaf(physicalID)
		if !ok || leaf != physicalID+"-fast" {
			t.Fatalf("fast leaf for %q = %q, %v", physicalID, leaf, ok)
		}
		if got, ok := TrustedCodexPhysicalFromFastLeaf(leaf); !ok || got != physicalID {
			t.Fatalf("fast leaf reverse lookup = %q, %v, want %q", got, ok, physicalID)
		}
		if got, ok := TrustedCodexPhysicalFromStandardAlias(standard); !ok || got != physicalID {
			t.Fatalf("standard alias reverse lookup = %q, %v, want %q", got, ok, physicalID)
		}
		if got, ok := TrustedCodexPhysicalFromFastAlias(fast); !ok || got != physicalID {
			t.Fatalf("fast alias reverse lookup = %q, %v, want %q", got, ok, physicalID)
		}
	}

	for _, untrusted := range []string{
		"gpt-5.6-unknown",
		"gpt-5.6-terra-fast",
		"GPT-5.6-TERRA",
		"prefix-gpt-5.6-terra",
		"gpt-5.6-terra-suffix",
	} {
		if IsTrustedCodexPhysicalModel(untrusted) {
			t.Fatalf("untrusted physical model %q was accepted", untrusted)
		}
		if _, ok := CodexStandardAlias(untrusted); ok {
			t.Fatalf("untrusted physical model %q received a standard alias", untrusted)
		}
		if _, ok := CodexFastAlias(untrusted); ok {
			t.Fatalf("untrusted physical model %q received a fast alias", untrusted)
		}
	}

	if !IsTrustedCodexPhysicalModel("  gpt-5.6-terra  ") {
		t.Fatal("registry should ignore surrounding transport whitespace")
	}
	if _, ok := TrustedCodexPhysicalFromStandardAlias("subscription/codex/gpt-5.6-terra-fast"); ok {
		t.Fatal("fast alias was accepted as a standard alias")
	}
	if _, ok := TrustedCodexPhysicalFromFastAlias("subscription/codex/gpt-5.6-terra-fast-fast"); ok {
		t.Fatal("recursive fast alias was accepted")
	}
	if _, ok := TrustedCodexPhysicalFromFastLeaf("gpt-5.6-terra-fast-fast"); ok {
		t.Fatal("recursive fast leaf was accepted")
	}
}

func TestTrustedCodexReasoningEffortsAreExactAndCopied(t *testing.T) {
	want := []string{"low", "medium", "high", "xhigh", "max"}
	first := TrustedCodexReasoningEfforts()
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("reasoning efforts = %v, want %v", first, want)
	}
	first[0] = "mutated"
	if got := TrustedCodexReasoningEfforts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("registry reasoning efforts were mutated through returned slice: %v", got)
	}
}
