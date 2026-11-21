package main

import (
	"testing"

	"streamcheck/internal/build"
	"streamcheck/internal/fixture"
)

func mustTiers(t *testing.T, spec string) []fixture.Tier {
	t.Helper()
	tiers, err := parseTiers(spec)
	if err != nil {
		t.Fatal(err)
	}
	return tiers
}

func TestParseTiers(t *testing.T) {
	tiers, err := parseTiers("450k,900k,1800k")
	if err != nil {
		t.Fatal(err)
	}
	if len(tiers) != 3 {
		t.Fatalf("count: %d", len(tiers))
	}
	want := map[string]int{"450k": 450_000, "900k": 900_000, "1800k": 1_800_000}
	for _, tier := range tiers {
		if tier.BandwidthBPS != want[tier.Name] {
			t.Fatalf("%s bps: %d", tier.Name, tier.BandwidthBPS)
		}
	}
}

func TestParseBitrateSuffixes(t *testing.T) {
	cases := map[string]int{
		"450k": 450_000, "2M": 2_000_000, "128k": 128_000,
	}
	for in, want := range cases {
		got, err := parseBitrate(in)
		if err != nil || got != want {
			t.Fatalf("%s: got %d, %v", in, got, err)
		}
	}
	if _, err := parseBitrate("abc"); err == nil {
		t.Fatal("invalid bitrate must error")
	}
	if _, err := parseTiers("900k,900k"); err == nil {
		t.Fatal("duplicate tier must error")
	}
}

func TestParseDropSpec(t *testing.T) {
	tier, idx, err := parseDropSpec("900k:4")
	if err != nil || tier != "900k" || idx != 4 {
		t.Fatalf("got %s %d %v", tier, idx, err)
	}
	if _, _, err := parseDropSpec("nodelimiter"); err == nil {
		t.Fatal("bad drop spec must error")
	}
}

func TestParseSwapSpec(t *testing.T) {
	tier, a, b, err := parseSwapSpec("1800k:2:3")
	if err != nil || tier != "1800k" || a != 2 || b != 3 {
		t.Fatalf("got %s %d %d %v", tier, a, b, err)
	}
	if _, _, _, err := parseSwapSpec("1800k:2"); err == nil {
		t.Fatal("bad swap spec must error")
	}
}

func TestValidateFaultTiers(t *testing.T) {
	tiers := mustTiers(t, "450k,900k")
	faults := map[string]build.TierFault{"450k": {HasDrop: true, DropSegment: 1}}
	if err := validateFaultTiers(tiers, faults); err != nil {
		t.Fatalf("valid fault rejected: %v", err)
	}
	bad := map[string]build.TierFault{"unknown": {HasDrop: true}}
	if err := validateFaultTiers(tiers, bad); err == nil {
		t.Fatal("unknown tier fault must error")
	}
}
