package repair_test

import (
	"testing"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/repair"
)

// syntheticTrack builds an in-memory demux track with an explicit DTS sequence.
// stts cannot encode negative deltas, so a genuine backward jump (the primary
// failure mode after a primary/backup source switch) is exercised here rather
// than through a serialized fixture.
func syntheticTrack(dts []int64, ts uint32) *demux.Track {
	tr := &demux.Track{ID: 7, Timescale: ts, Video: true}
	for i, t := range dts {
		tr.Samples = append(tr.Samples, demux.Sample{
			Index:      uint32(i),
			DTS:        clock.MustMake(t, int64(ts)),
			Duration:   clock.MustMake(1, int64(ts)),
			FileOffset: uint64(i * 64),
			Size:       64,
			Sync:       i%2 == 0,
			Present:    true,
		})
	}
	return tr
}

func TestRepairBackwardJumpFromSourceSwitch(t *testing.T) {
	// A backup source restarts its clock: timestamps climb, then jump back.
	tr := syntheticTrack([]int64{100, 101, 102, 50, 51, 52}, 25)
	res, err := repair.One(tr, clock.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	sawBackward := false
	for i, s := range res.Samples {
		if i > 0 && s.Value.Cmp(res.Samples[i-1].Value) <= 0 {
			t.Fatalf("row %d not monotonic: %s", i, s.Value)
		}
		if s.Reason == clock.ReasonBackward {
			sawBackward = true
		}
	}
	if !sawBackward {
		t.Fatal("expected a backward_timestamp reason after the source switch")
	}
	// The post-switch rows continue the programme timeline, not the backup's.
	if res.Samples[3].Value.Cmp(clock.MustMake(103, 25)) != 0 {
		t.Fatalf("first post-switch sample at %s, want 103/25", res.Samples[3].Value)
	}
}

func TestRepairRejectsZeroTimescale(t *testing.T) {
	tr := syntheticTrack([]int64{1, 2}, 25)
	tr.Timescale = 0
	_, err := repair.One(tr, clock.DefaultPolicy())
	if err == nil {
		t.Fatal("expected zero-timescale rejection")
	}
}

func TestVerifyMonotonicDetectsRegression(t *testing.T) {
	good := &repair.RepairedTrack{
		Samples: []clock.Repaired{
			{Value: clock.MustMake(0, 25)},
			{Value: clock.MustMake(1, 25)},
		},
	}
	if err := repair.VerifyMonotonic(good); err != nil {
		t.Fatalf("good track flagged: %v", err)
	}
	bad := &repair.RepairedTrack{
		Samples: []clock.Repaired{
			{Value: clock.MustMake(1, 25)},
			{Value: clock.MustMake(1, 25)},
		},
	}
	if err := repair.VerifyMonotonic(bad); err == nil {
		t.Fatal("duplicate timestamp must fail verification")
	}
}
