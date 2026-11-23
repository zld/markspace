package manifest

import (
	"errors"
	"testing"

	"mediad/internal/clock"
)

const (
	testTS       uint32 = 90000
	testSeg             = int64(180000)  // 2 s at 90 kHz
	testDuration        = int64(1080000) // 12 s => 6 segments
)

// healthyLayer builds nPackets packets with a keyframe every keyEvery master
// ticks starting at each segment boundary.
func healthyLayer(idx int, name string, targetBPS, nPackets int, keyEvery int64, size int) LayerInput {
	pkts := make([]ClockedPacket, nPackets)
	step := testDuration / int64(nPackets)
	for i := 0; i < nPackets; i++ {
		ts := int64(i) * step
		pkts[i] = NewPacket(uint16(i), ts, ts%keyEvery == 0, size)
	}
	return LayerInput{Index: idx, Name: name, TargetBPS: targetBPS, Packets: pkts,
		LateKeys: map[int]bool{}}
}

func TestBuildSegmentCountAndAlignment(t *testing.T) {
	b, err := NewBuilder(testTS, testSeg, testSeg/2, testDuration)
	if err != nil {
		t.Fatal(err)
	}
	// Tune packet size so actual bitrate lands near each target.
	inputs := []LayerInput{
		healthyLayer(0, "450k", 450000, 600, testSeg, 1125),
		healthyLayer(1, "900k", 900000, 600, testSeg, 2250),
		healthyLayer(2, "1800k", 1800000, 600, testSeg, 4500),
	}
	m, err := b.Build(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 6 {
		t.Fatalf("segments = %d, want 6", len(m.Segments))
	}
	if len(m.Representations) != 3 {
		t.Fatalf("representations = %d, want 3", len(m.Representations))
	}
	for i, seg := range m.Segments {
		if seg.StartTick != int64(i)*testSeg {
			t.Fatalf("segment %d start %d off the shared boundary", i, seg.StartTick)
		}
		for li, present := range seg.Present {
			if !present {
				t.Fatalf("healthy layer %d missing segment %d", li, i)
			}
		}
	}
	if err := Verify(m, 3); err != nil {
		t.Fatalf("healthy manifest must verify: %v", err)
	}
}

func TestLateKeyframeMarksOnlyThatLayer(t *testing.T) {
	b, _ := NewBuilder(testTS, testSeg, testSeg/4, testDuration)
	base := healthyLayer(0, "450k", 450000, 600, testSeg, 1125)
	late := healthyLayer(1, "900k", 900000, 600, testSeg, 2250)
	// Find the first keyframe packet at the segment-2 boundary and mark it late.
	lateStart := 2 * testSeg
	for i, p := range late.Packets {
		if p.TS == lateStart && p.Key {
			late.LateKeys[i] = true
			break
		}
	}
	good := healthyLayer(2, "1800k", 1800000, 600, testSeg, 4500)
	m, err := b.Build([]LayerInput{base, late, good})
	if err != nil {
		t.Fatal(err)
	}
	seg2 := m.Segments[2]
	if seg2.Present[1] {
		t.Fatal("layer with late keyframe must be marked missing for segment 2")
	}
	if !seg2.Present[0] || !seg2.Present[2] {
		t.Fatal("other layers must keep advancing past the late keyframe")
	}
	if len(seg2.MissingLayer) != 1 || seg2.MissingLayer[0] != 1 {
		t.Fatalf("missing-layer vector wrong: %v", seg2.MissingLayer)
	}
	// Segment 3 of the late layer recovers on its own boundary.
	if !m.Segments[3].Present[1] {
		t.Fatal("layer must recover at the next segment boundary")
	}
}

func TestDeterministicSummary(t *testing.T) {
	b, _ := NewBuilder(testTS, testSeg, testSeg/2, testDuration)
	build := func() *Manifest {
		m, err := b.Build([]LayerInput{
			healthyLayer(0, "450k", 450000, 600, testSeg, 1125),
			healthyLayer(1, "900k", 900000, 600, testSeg, 2250),
			healthyLayer(2, "1800k", 1800000, 600, testSeg, 4500),
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	first, second := build(), build()
	if first.Summary != second.Summary {
		t.Fatalf("non-deterministic summary:\n%s\n%s", first.Summary, second.Summary)
	}
	if first.AlignmentErrorMS != second.AlignmentErrorMS {
		t.Fatal("alignment stats must be reproducible")
	}
}

func TestVerifyRejectsExcessiveBitrateError(t *testing.T) {
	b, _ := NewBuilder(testTS, testSeg, testSeg/2, testDuration)
	// Tiny packets => actual bitrate far under a huge target.
	in := healthyLayer(0, "x", 50_000_000, 600, testSeg, 10)
	m, err := b.Build([]LayerInput{in})
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(m, 1); err == nil {
		t.Fatal("manifest exceeding bitrate error threshold must fail verify")
	} else {
		var ce *clock.Error
		if !errors.As(err, &ce) || !ce.Recoverable {
			t.Fatalf("threshold violation must be recoverable, got %v", err)
		}
	}
}

func TestQualityReflectsLossAndDrops(t *testing.T) {
	b, _ := NewBuilder(testTS, testSeg, testSeg/2, testDuration)
	in := healthyLayer(0, "450k", 450000, 540, testSeg, 1125)
	in.Lost = 30
	in.Dropped = 30 // 600 expected, 540 delivered => quality 0.9
	m, err := b.Build([]LayerInput{in})
	if err != nil {
		t.Fatal(err)
	}
	q := m.Representations[0].Quality
	if q < 0.89 || q > 0.91 {
		t.Fatalf("quality = %.4f, want ~0.90", q)
	}
	if QualityMean(m) != q {
		t.Fatal("mean quality mismatch")
	}
}

func TestBuilderRejectsBadParams(t *testing.T) {
	if _, err := NewBuilder(0, testSeg, 0, testDuration); err == nil {
		t.Fatal("zero timescale must be rejected")
	}
	if _, err := NewBuilder(testTS, testSeg, testSeg+1, testDuration); err == nil {
		t.Fatal("wait budget beyond one segment must be rejected")
	}
	if _, err := NewBuilder(testTS, testSeg, 0, 0); err == nil {
		t.Fatal("zero duration must be rejected")
	}
}

func TestRecoveredAndMissingCounters(t *testing.T) {
	b, _ := NewBuilder(testTS, testSeg, testSeg/4, testDuration)
	late := healthyLayer(0, "450k", 450000, 600, testSeg, 1125)
	late.Recovered = 1
	for i, p := range late.Packets {
		if p.TS == testSeg && p.Key {
			late.LateKeys[i] = true
		}
	}
	m, err := b.Build([]LayerInput{late})
	if err != nil {
		t.Fatal(err)
	}
	if RecoveredLayers(m) != 1 {
		t.Fatal("recovered layer count wrong")
	}
	if TotalMissingSegments(m) != 1 {
		t.Fatalf("missing segments = %d, want 1", TotalMissingSegments(m))
	}
}
