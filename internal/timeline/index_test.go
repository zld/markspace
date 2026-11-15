package timeline

import (
	"testing"

	"hlsforge/internal/ingest"
	"hlsforge/internal/report"
)

func frag(bitrate, seq int, base, dur uint64) *ingest.Fragment {
	return &ingest.Fragment{Bitrate: bitrate, Seq: seq, BaseTime: base, Duration: dur}
}

func ingestTrack(bitrate int, seqs map[int]*ingest.Fragment) *ingest.TrackIngest {
	t := &ingest.TrackIngest{
		Bitrate: bitrate, Timescale: 1000, PartMs: 500,
		FromSeq: 1, ToSeq: 4, Slots: map[int]*ingest.Fragment{},
	}
	for seq, f := range seqs {
		t.Slots[seq] = f
	}
	return t
}

func TestBuildKeepsGapSlotsAndIntegerScale(t *testing.T) {
	in := ingestTrack(800000, map[int]*ingest.Fragment{
		1: frag(800000, 1, 0, 500),
		2: frag(800000, 2, 500, 500),
		// seq 3 intentionally missing
		4: frag(800000, 4, 1500, 500),
	})
	tr, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Parts) != 4 {
		t.Fatalf("parts = %d, want 4 (gap retained)", len(tr.Parts))
	}
	if !tr.Parts[2].Missing || tr.Parts[2].StartMs != 1000 || tr.Parts[2].Seq != 3 {
		t.Fatalf("gap slot wrong: %+v", tr.Parts[2])
	}
	// seq 4 must not be shifted forward into the gap.
	if tr.Parts[3].Fragment == nil || tr.Parts[3].StartMs != 1500 {
		t.Fatalf("later fragment shifted: %+v", tr.Parts[3])
	}
	if tr.GapCount() != 1 || tr.StartMs != 0 || tr.EndMs != 2000 {
		t.Fatalf("track summary wrong: gaps=%d start=%d end=%d",
			tr.GapCount(), tr.StartMs, tr.EndMs)
	}
}

func TestBuildIntegerScaleConversion(t *testing.T) {
	// Timescale 90000: 45000 units == 500ms exactly, no float involved.
	in := &ingest.TrackIngest{
		Bitrate: 800000, Timescale: 90000, PartMs: 500,
		FromSeq: 1, ToSeq: 1,
		Slots: map[int]*ingest.Fragment{1: {
			Bitrate: 800000, Seq: 1, BaseTime: 90000, Duration: 45000,
		}},
	}
	tr, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Parts[0].StartMs != 1000 || tr.Parts[0].DurationMs != 500 {
		t.Fatalf("scale conversion wrong: %+v", tr.Parts[0])
	}
}

func TestCompareDetectsDrift(t *testing.T) {
	// Reference is the highest-bitrate track; the 800k track carries drift.
	low, err := Build(ingestTrack(800000, map[int]*ingest.Fragment{
		1: frag(800000, 1, 80, 500),
		2: frag(800000, 2, 580, 500),
	}))
	if err != nil {
		t.Fatal(err)
	}
	high, err := Build(ingestTrack(1600000, map[int]*ingest.Fragment{
		1: frag(1600000, 1, 0, 500),
		2: frag(1600000, 2, 500, 500),
	}))
	if err != nil {
		t.Fatal(err)
	}
	// Pass the low track first to prove ordering does not choose the reference.
	findings, maxDev := Compare([]*Track{low, high}, 50)
	if maxDev != 80 {
		t.Fatalf("max deviation = %d, want 80", maxDev)
	}
	if len(findings) != 1 || findings[0].Code != report.CodeTimelineDrift ||
		findings[0].Bitrate != 800000 {
		t.Fatalf("findings = %+v", findings)
	}
	// The reference high track's own deviation against itself is zero.
	if dev := Deviation(high, high); dev != 0 {
		t.Fatalf("self deviation = %d", dev)
	}
	// Within tolerance: no findings.
	if fs, _ := Compare([]*Track{low, high}, 80); len(fs) != 0 {
		t.Fatalf("deviation at tolerance boundary must pass: %+v", fs)
	}
}

func TestCompareReferenceIsFirstContentTrack(t *testing.T) {
	empty, err := Build(ingestTrack(800000, map[int]*ingest.Fragment{}))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Build(ingestTrack(1600000, map[int]*ingest.Fragment{
		1: frag(1600000, 1, 0, 500),
	}))
	if err != nil {
		t.Fatal(err)
	}
	findings, maxDev := Compare([]*Track{empty, ok}, 50)
	if len(findings) != 0 || maxDev != 0 {
		t.Fatalf("empty track must be skipped as reference: %+v", findings)
	}
}

func TestBuildRejectsZeroTimescale(t *testing.T) {
	in := ingestTrack(800000, map[int]*ingest.Fragment{1: frag(800000, 1, 0, 1)})
	in.Timescale = 0
	if _, err := Build(in); err == nil {
		t.Fatal("zero timescale must error")
	}
}
