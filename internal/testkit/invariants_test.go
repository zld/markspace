package testkit

import (
	"context"
	"os"
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/index"
	"mediacore/internal/scan"
)

// TestCrossStageInvariants exercises the four cross-stage invariants named in
// the spec in one place: sample ordering, monotonic time, index offset range
// and manifest start/end alignment.
func TestCrossStageInvariants(t *testing.T) {
	fs := generateNormal(t, t.TempDir())
	if _, err := RunPipeline(context.Background(), fs, basePipeline(), "", nil); err != nil {
		t.Fatal(err)
	}
	m, digest, err := scan.LoadManifest(fs.MP4, fs.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	idxPath := index.CachePath(fs.Index, digest, 1)
	f, err := os.Open(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hdr, err := index.LoadOrVerify(idxPath, digest, 1, demoSamples)
	if err != nil {
		t.Fatal(err)
	}
	mp4Info, err := os.Stat(fs.MP4)
	if err != nil {
		t.Fatal(err)
	}
	var prevDTS int64
	var prevOffset uint64
	for i := uint32(0); i < hdr.SampleCount; i++ {
		rec, err := index.ReadRecord(f, hdr, i)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		// 1. sample ordering: physical offsets are non-decreasing.
		if i > 0 && rec.Offset < prevOffset {
			t.Fatalf("offset regression at sample %d: %d < %d", i, rec.Offset, prevOffset)
		}
		prevOffset = rec.Offset
		// 2. monotonic time on the unified timeline.
		if i > 0 && rec.DTSNanos <= prevDTS {
			t.Fatalf("non-monotonic DTS at sample %d: %d <= %d", i, rec.DTSNanos, prevDTS)
		}
		prevDTS = rec.DTSNanos
		// 3. index offset range: every payload lies inside the MP4 file.
		end := rec.Offset + uint64(rec.Size)
		if end > uint64(mp4Info.Size()) || end < rec.Offset {
			t.Fatalf("sample %d offset [%d,%d) outside file size %d",
				i, rec.Offset, end, mp4Info.Size())
		}
	}
	// 4. manifest window: observed timeline matches the declared start/end
	// within the alignment tolerance.
	first, err := index.ReadRecord(f, hdr, 0)
	if err != nil {
		t.Fatal(err)
	}
	last, err := index.ReadRecord(f, hdr, hdr.SampleCount-1)
	if err != nil {
		t.Fatal(err)
	}
	if clock.Nanos(first.DTSNanos) != clock.Nanos(m.StartNanos) {
		t.Fatalf("first DTS %d != manifest start %d", first.DTSNanos, m.StartNanos)
	}
	basis := clock.TimeBase{Timescale: demoTimescale}
	frameNanos, _ := basis.ToNanos(int64(demoDelta))
	observedEnd := clock.Nanos(last.DTSNanos) + frameNanos
	if got := abs64n(int64(observedEnd) - m.EndNanos); got > 2_000_000 {
		t.Fatalf("manifest end error %d ns exceeds 2ms", got)
	}
	if hdr.SampleCount != m.SampleCount {
		t.Fatalf("index count %d != manifest count %d", hdr.SampleCount, m.SampleCount)
	}
}

func abs64n(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
