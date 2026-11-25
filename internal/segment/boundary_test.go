package segment

import (
	"testing"

	"packcheck/internal/clock"
	"packcheck/internal/container"
	"packcheck/internal/model"
)

func testRate(t *testing.T) clock.Rate {
	t.Helper()
	r, err := clock.ParseRate("25")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestValidateRTPHealthy(t *testing.T) {
	// 25fps on a 90kHz clock: one frame spans 3600 ticks.
	pkts, err := BuildPackets(50, 0, 3600, 1234)
	if err != nil {
		t.Fatal(err)
	}
	res := ValidateRTP(pkts, testRate(t))
	if res.Issues.HasError() {
		t.Fatalf("healthy stream flagged: %v", res.Issues.All())
	}
	if len(res.Frames) != 50 {
		t.Errorf("frames=%d want 50", len(res.Frames))
	}
	if res.Frames[1].Start.Cmp(clock.MustRat(3600, 90000)) != 0 {
		t.Errorf("instant=%s want 3600/90000", res.Frames[1].Start)
	}
}

func TestValidateRTPDetectsGapAndRollback(t *testing.T) {
	pkts, _ := BuildPackets(10, 0, 3600, 1)
	// Drop the middle packet to create a sequence gap.
	gapped := append([]Packet{}, pkts[:4]...)
	gapped = append(gapped, pkts[5:]...)
	res := ValidateRTP(gapped, testRate(t))
	if res.Issues.CountByCode()[model.CodeBoundaryDrift] != 1 {
		t.Errorf("gap detection failed: %v", res.Issues.All())
	}

	// Rewind one timestamp to force rollback.
	rolled := append([]Packet{}, pkts...)
	rolled[6] = Packet{Raw: mustRewriteTS(t, pkts[6].Raw, 100), ByteOffset: pkts[6].ByteOffset}
	res2 := ValidateRTP(rolled, testRate(t))
	if res2.Issues.CountByCode()[model.CodeWraparound] != 1 {
		t.Errorf("rollback detection failed: %v", res2.Issues.All())
	}
	if res2.Issues.All()[0].ByteOffset != pkts[6].ByteOffset {
		t.Error("issue must carry the packet byte offset")
	}
}

func TestMalformedPacketRejected(t *testing.T) {
	pkts := []Packet{{Raw: []byte{0x00}, ByteOffset: 7}}
	res := ValidateRTP(pkts, testRate(t))
	if res.Issues.CountByCode()[model.CodeUnsupportedInput] != 1 {
		t.Errorf("malformed packet not rejected: %v", res.Issues.All())
	}
}

func TestCompareToDeclared(t *testing.T) {
	rate := testRate(t)
	pkts, _ := BuildPackets(100, 0, 3600, 1)
	res := ValidateRTP(pkts, rate)

	healthy := &container.Container{
		MovieTimescale: 25,
		Boundaries: []container.Boundary{
			{StartTick: 0, EndTick: 25},
			{StartTick: 25, EndTick: 50},
		},
	}
	CompareToDeclared(res, healthy)
	if res.Issues.HasError() {
		t.Errorf("aligned boundaries flagged: %v", res.Issues.All())
	}

	// A truncated layer loses one fragment: its declared boundary no longer
	// has a matching RTP packet even though the timestamp grid is intact.
	gappedPkts := append([]Packet{}, pkts[:25]...)
	gappedPkts = append(gappedPkts, pkts[26:]...)
	res2 := ValidateRTP(gappedPkts, rate)
	CompareToDeclared(res2, healthy)
	// One issue for the sequence gap, one for the declared boundary with
	// no matching fragment after the loss.
	if got := res2.Issues.CountByCode()[model.CodeBoundaryDrift]; got != 2 {
		t.Errorf("boundary drift count=%d want 2: %v", got, res2.Issues.All())
	}
}

func TestCrossCheck(t *testing.T) {
	base := []uint32{0, 3600, 7200}
	aligned, _ := CrossCheck([]LayerSignature{
		{Representation: "low", Timestamps: base, Healthy: true},
		{Representation: "mid", Timestamps: base, Healthy: true},
		{Representation: "high", Timestamps: base, Healthy: true},
	})
	if !aligned {
		t.Error("identical signatures must align")
	}
	ok, issues := CrossCheck([]LayerSignature{
		{Representation: "low", Timestamps: base, Healthy: true},
		{Representation: "mid", Timestamps: []uint32{0, 3600, 9999}, Healthy: true},
		{Representation: "high", Timestamps: base, Healthy: false}, // isolated layer ignored
	})
	if ok || len(issues) != 1 || issues[0].Representation != "mid" {
		t.Errorf("cross-check result wrong: ok=%v issues=%v", ok, issues)
	}
}
