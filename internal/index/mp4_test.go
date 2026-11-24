package index

import (
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/repair"
)

func mkSamples(n int) ([]repair.Sample, []int64) {
	s := make([]repair.Sample, n)
	d := make([]int64, n)
	for i := 0; i < n; i++ {
		s[i] = repair.Sample{
			Index: i, Seq: uint16(i), MediaNS: int64(i) * 100,
			IsKeyframe: i%5 == 0, SizeBytes: 40 + i,
		}
		d[i] = 100
	}
	return s, d
}

func TestBuildAndVerifyRoundTrip(t *testing.T) {
	samples, durations := mkSamples(20)
	b := NewBuilder(1)
	idx, data, err := b.Build(samples, durations, clock.DefaultClockRate)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 20 || len(idx.RandomAccess) != 4 {
		t.Fatalf("entries=%d rap=%d", len(idx.Entries), len(idx.RandomAccess))
	}
	rep, err := Verify(data, idx)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasFTYP || !rep.HasMOOV || !rep.HasMDAT {
		t.Fatal("required boxes missing on independent parse")
	}
	if !rep.EntriesInBounds || rep.BoundaryErrors != 0 || !rep.RandomAccessOK {
		t.Fatalf("verification failed: %+v", rep)
	}
}

func TestIndexDeterministicHash(t *testing.T) {
	samples, durations := mkSamples(15)
	b := NewBuilder(2)
	i1, d1, e1 := b.Build(samples, durations, clock.DefaultClockRate)
	i2, d2, e2 := b.Build(samples, durations, clock.DefaultClockRate)
	if e1 != nil || e2 != nil {
		t.Fatal("build failed")
	}
	if i1.SHA256 != i2.SHA256 {
		t.Fatal("non-deterministic index hash")
	}
	if string(d1) != string(d2) {
		t.Fatal("non-deterministic serialized bytes")
	}
}

func TestIsolatedSamplesExcluded(t *testing.T) {
	samples, durations := mkSamples(10)
	// Mark a middle sample isolated and another synthetic.
	samples[4].Isolated = true
	samples[7].Synthetic = true
	b := NewBuilder(1)
	idx, data, err := b.Build(samples, durations, clock.DefaultClockRate)
	if err != nil {
		t.Fatal(err)
	}
	if idx.RejectedSamples != 2 {
		t.Fatalf("rejected=%d want 2", idx.RejectedSamples)
	}
	if idx.FirstFailure == nil || idx.FirstFailure.Sequence != 4 {
		t.Fatalf("first failure not reported: %+v", idx.FirstFailure)
	}
	for _, e := range idx.Entries {
		if e.SampleIndex == 4 || e.SampleIndex == 7 {
			t.Fatal("isolated/synthetic sample leaked into index")
		}
	}
	rep, err := Verify(data, idx)
	if err != nil || !rep.EntriesInBounds {
		t.Fatalf("excluded sample broke verification: %v %+v", err, rep)
	}
}

func TestZeroSizeSampleRejected(t *testing.T) {
	samples, durations := mkSamples(5)
	samples[2].SizeBytes = 0
	b := NewBuilder(1)
	idx, _, err := b.Build(samples, durations, clock.DefaultClockRate)
	if err != nil {
		t.Fatal(err)
	}
	if idx.RejectedSamples != 1 {
		t.Fatalf("zero-size sample not rejected: %d", idx.RejectedSamples)
	}
}

func TestOffsetsContiguousAndBounded(t *testing.T) {
	samples, durations := mkSamples(12)
	b := NewBuilder(1)
	idx, data, err := b.Build(samples, durations, clock.DefaultClockRate)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) > clock.MaxMP4FileBytes {
		t.Fatal("file exceeds global bound")
	}
	for i := 1; i < len(idx.Entries); i++ {
		prevEnd := idx.Entries[i-1].Offset + idx.Entries[i-1].Size
		if idx.Entries[i].Offset != prevEnd {
			t.Fatalf("entry %d not contiguous: gap=%d", i, idx.Entries[i].Offset-prevEnd)
		}
	}
	last := idx.Entries[len(idx.Entries)-1]
	if last.Offset+last.Size > idx.MDATDataOffset+idx.MDATPayloadBytes {
		t.Fatal("last entry runs past mdat payload")
	}
}

func TestVerifyRejectsTamperedIndex(t *testing.T) {
	samples, durations := mkSamples(8)
	b := NewBuilder(1)
	idx, data, err := b.Build(samples, durations, clock.DefaultClockRate)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt one offset to point beyond the file; verification must catch it.
	idx.Entries[3].Offset = int64(len(data)) + 100
	rep, err := Verify(data, idx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.EntriesInBounds || rep.BoundaryErrors == 0 {
		t.Fatal("tampered offset was not detected by independent parser")
	}
}
