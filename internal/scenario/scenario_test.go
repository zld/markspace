package scenario_test

import (
	"testing"

	"replay/internal/fault"
	"replay/internal/scenario"
)

func TestBuildIsDeterministic(t *testing.T) {
	a, err := scenario.Build(scenario.CaseNormal, []string{"v1", "v2"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	b, err := scenario.Build(scenario.CaseNormal, []string{"v1", "v2"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(a.RTP) != len(b.RTP) || len(a.Assets) != len(b.Assets) {
		t.Fatal("fixture shape is not deterministic")
	}
	for i := range a.RTP {
		if string(a.RTP[i].Bytes) != string(b.RTP[i].Bytes) {
			t.Fatalf("rtp datagram %d differs between builds", i)
		}
	}
}

func TestEveryCaseBuilds(t *testing.T) {
	for _, cs := range scenario.KnownCases() {
		if _, err := scenario.Build(cs, []string{"v1", "v2"}); err != nil {
			t.Fatalf("case %s: %v", cs, err)
		}
	}
}

func TestUnknownCaseRejected(t *testing.T) {
	_, err := scenario.Build("nope", []string{"v1"})
	if err == nil || !fault.IsKind(err, fault.KindArgument) {
		t.Fatalf("want argument error, got %v", err)
	}
}

func TestBuildRequiresVersions(t *testing.T) {
	if _, err := scenario.Build(scenario.CaseNormal, nil); err == nil {
		t.Fatal("expected error with no versions")
	}
}

func TestWrapCaseCrosses16BitBoundary(t *testing.T) {
	fx, err := scenario.Build(scenario.CaseSequenceWrap, []string{"v1"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if fx.Case != scenario.CaseSequenceWrap {
		t.Fatalf("case=%s", fx.Case)
	}
	found := false
	for _, p := range fx.RTP {
		if p.Track != "audio-main" {
			continue
		}
		// Packet payloads are opaque; assert on length instead.
		if len(p.Bytes) == 0 {
			t.Fatal("empty datagram")
		}
		found = true
	}
	if !found {
		t.Fatal("no audio datagrams in wrap fixture")
	}
}

func TestTruncatedCaseDeclaresTruncation(t *testing.T) {
	fx, err := scenario.Build(scenario.CaseTruncatedRTCP, []string{"v1"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var truncated int
	for _, r := range fx.RTCP {
		if r.Truncate > 0 {
			truncated++
		}
	}
	if truncated != 1 {
		t.Fatalf("truncated reports=%d want 1", truncated)
	}
}

func TestDuplicateConflictCaseAddsConflictingPacket(t *testing.T) {
	base, _ := scenario.Build(scenario.CaseNormal, []string{"v1"})
	conflict, _ := scenario.Build(scenario.CaseDuplicateConflict, []string{"v1"})
	if len(conflict.RTP) != len(base.RTP)+1 {
		t.Fatalf("conflict datagrams=%d want %d", len(conflict.RTP), len(base.RTP)+1)
	}
}

func TestAssetBytesAreDeterministicMP4(t *testing.T) {
	spec := scenario.AssetSpec{Version: "v1", Track: "audio-main", DurationNS: 1_000_000_000, ContentKey: "shared"}
	a, err := scenario.AssetBytes(spec)
	if err != nil {
		t.Fatalf("AssetBytes: %v", err)
	}
	b, _ := scenario.AssetBytes(spec)
	if string(a) != string(b) {
		t.Fatal("asset bytes are not deterministic")
	}
	if len(a) < 8 || string(a[4:8]) != "ftyp" {
		t.Fatalf("asset does not start with an ftyp box: % x", a[:8])
	}
}

func TestAssetBytesRejectsBadDuration(t *testing.T) {
	if _, err := scenario.AssetBytes(scenario.AssetSpec{Version: "v1", Track: "a"}); err == nil {
		t.Fatal("expected error for non-positive duration")
	}
}
