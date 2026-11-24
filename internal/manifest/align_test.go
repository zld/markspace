package manifest

import (
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/fanout"
	"mediasync/internal/fixture"
	"mediasync/internal/repair"
)

// buildTestLadder recovers a clean three-rendition stream for manifest tests.
func buildTestLadder(t *testing.T) (*fanout.Ladder, []repair.Sample, [][]int64) {
	t.Helper()
	cfg, err := fixture.DefaultConfig(41, 3_000_000_000, []int{1_200_000, 2_400_000, 4_800_000})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := fixture.Generate(cfg, fixture.CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	ladder, err := fanout.Recover(stream)
	if err != nil {
		t.Fatal(err)
	}
	durations := make([][]int64, len(ladder.Streams()))
	var all []repair.Sample
	for i, res := range ladder.Streams() {
		d, derr := repair.FrameDurations(res.Samples)
		if derr != nil {
			t.Fatal(derr)
		}
		durations[i] = d
		all = append(all, res.Samples...)
	}
	return ladder, all, durations
}

func TestManifestSegmentsAndCoverage(t *testing.T) {
	ladder, _, durations := buildTestLadder(t)
	b, err := NewBuilder(500_000_000, 33_333_333)
	if err != nil {
		t.Fatal(err)
	}
	m, err := b.Build(ladder, durations)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Renditions) != 3 {
		t.Fatalf("renditions=%d want 3", len(m.Renditions))
	}
	for _, rm := range m.Renditions {
		if len(rm.Segments) < 4 {
			t.Fatalf("too few segments: %d", len(rm.Segments))
		}
		for i := 1; i < len(rm.Segments); i++ {
			if rm.Segments[i].StartSample != rm.Segments[i-1].EndSample {
				t.Fatal("segments not contiguous")
			}
		}
		last := rm.Segments[len(rm.Segments)-1]
		if last.EndSample != len(ladder.Streams()[rm.Rendition].Samples) {
			t.Fatal("final segment does not cover all samples")
		}
	}
}

func TestManifestErrorWithinFramePeriod(t *testing.T) {
	ladder, _, durations := buildTestLadder(t)
	b, _ := NewBuilder(500_000_000, 33_333_333)
	m, err := b.Build(ladder, durations)
	if err != nil {
		t.Fatal(err)
	}
	if m.MaxErrorNS > b.framePeriodNS {
		t.Fatalf("alignment error %d ns exceeds frame period %d", m.MaxErrorNS, b.framePeriodNS)
	}
	if m.MaxErrorMS() > 33.4 {
		t.Fatalf("manifest error %.2f ms too large", m.MaxErrorMS())
	}
}

func TestManifestRejectsBadSwitchPoints(t *testing.T) {
	cfg, _ := fixture.DefaultConfig(7, 2_000_000_000, []int{1_000_000, 2_000_000})
	cfg.KeyframeOffset = []int{0, 7} // force GOP phase skew
	stream, _ := fixture.Generate(cfg, fixture.CaseClean)
	ladder, err := fanout.Recover(stream)
	if err != nil {
		t.Fatal(err)
	}
	durations := make([][]int64, len(ladder.Streams()))
	for i, res := range ladder.Streams() {
		d, _ := repair.FrameDurations(res.Samples)
		durations[i] = d
	}
	b, _ := NewBuilder(500_000_000, 33_333_333)
	m, err := b.Build(ladder, durations)
	if err != nil {
		t.Fatal(err)
	}
	if m.RejectedPoints == 0 {
		t.Fatal("skewed keyframes produced no rejected manifest points")
	}
}

func TestManifestBuilderValidation(t *testing.T) {
	if _, err := NewBuilder(0, 33_000_000); err == nil {
		t.Fatal("zero target duration accepted")
	}
	if _, err := NewBuilder(500_000_000, 0); err == nil {
		t.Fatal("zero frame period accepted")
	}
}

func TestManifestEmptyLadderRejected(t *testing.T) {
	b, _ := NewBuilder(500_000_000, 33_333_333)
	empty := &fanout.Ladder{}
	if _, err := b.Build(empty, nil); err == nil || err.Kind != clock.ErrInvalidLength {
		t.Fatalf("empty ladder classification wrong: %v", err)
	}
}
