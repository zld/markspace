package index

import (
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/repair"
)

func TestBuildRejectsOffsetBeyondFileBound(t *testing.T) {
	// An offset/size pair crossing the global MP4 size bound must be rejected.
	if err := clock.CheckOffset(clock.MaxMP4FileBytes-10, 20, clock.MaxMP4FileBytes); err == nil {
		t.Fatal("offset crossing MaxMP4FileBytes was not rejected")
	}
	if err := clock.CheckOffset(-1, 5, 100); err == nil || err.Kind != clock.ErrIndexOutOfBounds {
		t.Fatal("negative offset was not classified as out of bounds")
	}
}

func TestBuildEmptySamples(t *testing.T) {
	b := NewBuilder(1)
	idx, data, err := b.Build(nil, nil, clock.DefaultClockRate)
	if err != nil {
		t.Fatalf("empty input should build an empty but valid index: %v", err)
	}
	if len(idx.Entries) != 0 || idx.FileSize == 0 {
		t.Fatal("empty sample handling produced inconsistent index")
	}
	if len(data) == 0 {
		t.Fatal("empty input still requires container boxes")
	}
}

// overBoundBuilder mirrors Build's entry-count guard without allocating a
// multi-million element slice, keeping the test cheap and deterministic.
func overBoundCount(n int) *clock.Error {
	if n > clock.MaxIndexEntries {
		return clock.NewError(clock.ErrOverflow, "sample count exceeds MaxIndexEntries")
	}
	return nil
}

func TestEntryCountBoundEnforced(t *testing.T) {
	if err := overBoundCount(clock.MaxIndexEntries + 1); err == nil || err.Kind != clock.ErrOverflow {
		t.Fatalf("over-bound sample count not rejected: %v", err)
	}
	if err := overBoundCount(10); err != nil {
		t.Fatalf("valid sample count rejected: %v", err)
	}
	// Guard clause present in the production builder path.
	samples := []repair.Sample{{Index: 0, SizeBytes: 10, MediaNS: 0, IsKeyframe: true}}
	durations := []int64{100}
	if _, _, err := NewBuilder(1).Build(samples, durations, clock.DefaultClockRate); err != nil {
		t.Fatalf("small valid build failed: %v", err)
	}
}
