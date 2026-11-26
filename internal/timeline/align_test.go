package timeline

import (
	"testing"

	"github.com/pion/rtp"

	"continuity/internal/clock"
	"continuity/internal/manifest"
)

func seg(index, seq int, start, dur int64) manifest.Segment {
	return manifest.Segment{
		Index:    index,
		Sequence: seq,
		URL:      "seg",
		Start:    clock.FromInt(start),
		Duration: clock.FromInt(dur),
		ByteSize: 100,
		Line:     index + 1,
	}
}

func TestBuildNoGap(t *testing.T) {
	prim := &manifest.Representation{ID: "v", Segments: []manifest.Segment{
		seg(0, 1, 0, 2), seg(1, 2, 2, 2), seg(2, 3, 4, 2),
	}}
	tl, err := Build(prim, nil, "v")
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Segments) != 3 || tl.Recovered != 0 {
		t.Fatalf("tl = %s", tl)
	}
	if c, _ := tl.Segments[2].End.Cmp(clock.FromInt(6)); c != 0 {
		t.Errorf("end = %s", tl.Segments[2].End)
	}
	for _, s := range tl.Segments {
		if s.Source != SourcePrimary {
			t.Errorf("source = %s", s.Source)
		}
	}
}

func TestBuildGapFilledFromBackup(t *testing.T) {
	// Primary covers [0,2), gap [2,4), then [4,6).
	prim := &manifest.Representation{ID: "v", Segments: []manifest.Segment{
		seg(0, 1, 0, 2), seg(1, 3, 4, 2),
	}}
	// Backup covers the whole axis continuously, with a discontinuity at idx1.
	back := &manifest.Representation{
		ID: "v",
		Segments: []manifest.Segment{
			seg(0, 1, 0, 2), seg(1, 2, 2, 2), seg(2, 3, 4, 2),
		},
		Discontinuities: []int{1},
	}
	tl, err := Build(prim, back, "v")
	if err != nil {
		t.Fatal(err)
	}
	if tl.Recovered != 1 || len(tl.Gaps) != 1 {
		t.Fatalf("recovered=%d gaps=%d", tl.Recovered, len(tl.Gaps))
	}
	// Order: primary[0], backup gap-filler, primary[1].
	wantSources := []string{SourcePrimary, SourceBackup, SourcePrimary}
	for i, want := range wantSources {
		if tl.Segments[i].Source != want {
			t.Errorf("seg %d source = %s, want %s", i, tl.Segments[i].Source, want)
		}
	}
	filler := tl.Segments[1]
	if !filler.Discontinuity {
		t.Errorf("backup filler should carry discontinuity mark")
	}
	if c, _ := filler.Start.Cmp(clock.FromInt(2)); c != 0 {
		t.Errorf("filler start = %s", filler.Start)
	}
	if c, _ := tl.Segments[2].End.Cmp(clock.FromInt(6)); c != 0 {
		t.Errorf("program end = %s", tl.Segments[2].End)
	}
}

func TestBuildUnfillableNoBackup(t *testing.T) {
	prim := &manifest.Representation{ID: "v", Segments: []manifest.Segment{
		seg(0, 1, 0, 2), seg(1, 2, 4, 2),
	}}
	_, err := Build(prim, nil, "v")
	if clock.KindOf(err) != clock.KindUnrecoverable {
		t.Fatalf("want unrecoverable, got %v", err)
	}
}

func TestBuildUnfillableNoOverlap(t *testing.T) {
	prim := &manifest.Representation{ID: "v", Segments: []manifest.Segment{
		seg(0, 1, 0, 2), seg(1, 2, 4, 2),
	}}
	// Backup exists but covers [10,12): no temporal overlap with gap [2,4).
	back := &manifest.Representation{ID: "v", Segments: []manifest.Segment{
		seg(0, 9, 10, 2),
	}}
	_, err := Build(prim, back, "v")
	if clock.KindOf(err) != clock.KindUnrecoverable {
		t.Fatalf("want unrecoverable for non-overlapping backup, got %v", err)
	}
}

func TestBuildRejectsOverlappingPrimary(t *testing.T) {
	// Second segment starts at 3 while cursor is at 4 after [0,4) -> error.
	prim := &manifest.Representation{ID: "v", Segments: []manifest.Segment{
		seg(0, 1, 0, 4), seg(1, 2, 3, 2),
	}}
	if _, err := Build(prim, nil, "v"); clock.KindOf(err) != clock.KindInvalidInput {
		t.Fatalf("want invalid input for backwards start, got %v", err)
	}
}

func TestRTPWrapUnwrapping(t *testing.T) {
	// 48 kHz clock; timestamps climb to near 2^32 then wrap.
	packets := []*rtp.Packet{
		{Header: rtp.Header{SequenceNumber: 1, Timestamp: 0xFFFFFFF0}, Payload: []byte{1}},
		{Header: rtp.Header{SequenceNumber: 2, Timestamp: 0x00000010}, Payload: []byte{2}},
		{Header: rtp.Header{SequenceNumber: 3, Timestamp: 0x00000030}, Payload: []byte{3}},
	}
	out, err := ExpandRTPStream(packets)
	if err != nil {
		t.Fatal(err)
	}
	if out[0] != 0xFFFFFFF0 {
		t.Errorf("first = %#x", out[0])
	}
	// After wrap the unwrapped value must be 2^32 + 0x10.
	if out[1] != int64(0x100000000)+0x10 {
		t.Errorf("wrapped = %#x, want %#x", out[1], int64(0x100000000)+0x10)
	}
	if out[2]-out[1] != 0x20 {
		t.Errorf("delta = %#x", out[2]-out[1])
	}
}

func TestRTPDuplicateSequence(t *testing.T) {
	packets := []*rtp.Packet{
		{Header: rtp.Header{SequenceNumber: 5, Timestamp: 100}},
		{Header: rtp.Header{SequenceNumber: 5, Timestamp: 200}},
	}
	if _, err := ExpandRTPStream(packets); clock.KindOf(err) != clock.KindInvalidInput {
		t.Fatalf("want duplicate seq error, got %v", err)
	}
}

func TestUnwrapUnit(t *testing.T) {
	// Small forward step stays in cycle.
	if got := UnwrapTimestamp(1000, 1048); got != 1048 {
		t.Errorf("small step = %d", got)
	}
	// Backwards value within half-space is treated as a forward wrap.
	prev := int64(0x100000000) + 0x20
	got := UnwrapTimestamp(prev, 0x10)
	if got != prev-0x10 {
		t.Errorf("backwards = %d want %d", got, prev-0x10)
	}
}

func TestSwitchProgramTime(t *testing.T) {
	rec := SwitchRecord{Source: "backup", Seq: 42, Timestamp: 48000,
		ClockRate: 48000, ProgramNS: 2_000_000_000}
	prog, err := SwitchProgramTime(rec)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := prog.Cmp(clock.MustR(2, 1)); c != 0 {
		t.Errorf("program time = %s", prog)
	}
}
