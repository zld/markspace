package fixture

import (
	"bytes"
	"testing"

	"regress/internal/mp4"
	"regress/internal/rtp"
	"regress/internal/subtitle"
)

func TestDeterministicAcrossGenerations(t *testing.T) {
	a := Generate(19)
	b := Generate(19)
	if !bytes.Equal(a.SRTBase, b.SRTBase) || !bytes.Equal(a.MP4Healthy, b.MP4Healthy) {
		t.Fatal("same seed produced different bytes")
	}
	if len(a.RTPPackets) != len(b.RTPPackets) {
		t.Fatal("packet count differs across generations")
	}
	for i := range a.RTPPackets {
		if a.RTPPackets[i].SequenceNumber != b.RTPPackets[i].SequenceNumber {
			t.Fatalf("packet order differs at %d", i)
		}
	}
	if !bytes.Equal(a.MP4Corrupt, b.MP4Corrupt) || a.CorruptOffset != b.CorruptOffset {
		t.Fatal("corrupt fixture not deterministic")
	}
	// Different seed should change subtitle body content.
	c := Generate(20)
	if bytes.Equal(a.SRTBase, c.SRTBase) {
		t.Fatal("different seed produced identical SRT")
	}
}

func TestSingleCueModification(t *testing.T) {
	base, err := subtitle.Parse(bytes.NewReader(Generate(19).SRTBase), "srt")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := subtitle.Parse(bytes.NewReader(Generate(19).SRTModified), "srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Cues) != len(mod.Cues) {
		t.Fatal("cue count must be unchanged")
	}
	if base.PayloadDigest == mod.PayloadDigest {
		t.Fatal("single cue change must move payload digest")
	}
	// Timing is untouched, so start/end of every cue match.
	for i := range base.Cues {
		if base.Cues[i].Start.Cmp(mod.Cues[i].Start) != 0 ||
			base.Cues[i].End.Cmp(mod.Cues[i].End) != 0 {
			t.Fatal("cue timing changed despite payload-only edit")
		}
	}
}

func TestHealthyMP4ScansClean(t *testing.T) {
	a := Generate(19)
	fs, err := mp4.ScanReader(bytes.NewReader(a.MP4Healthy), "fixture.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.TrackErrors) != 0 || len(fs.Tracks) != 2 {
		t.Fatalf("healthy fixture: tracks=%d errors=%v", len(fs.Tracks), fs.TrackErrors)
	}
}

func TestCorruptMP4ReportsExactOffset(t *testing.T) {
	a := Generate(19)
	if a.CorruptOffset <= 0 {
		t.Fatal("corrupt offset not recorded")
	}
	fs, err := mp4.ScanReader(bytes.NewReader(a.MP4Corrupt), "corrupt.mp4")
	if err != nil {
		t.Fatalf("corruption must be isolated, got hard error: %v", err)
	}
	if len(fs.TrackErrors) == 0 {
		t.Fatal("expected at least one structured track error")
	}
	found := false
	for _, te := range fs.TrackErrors {
		if te.Offset == a.CorruptOffset {
			found = true
		}
	}
	if !found {
		t.Fatalf("no error at exact offset %d: %+v", a.CorruptOffset, fs.TrackErrors)
	}
}

func TestRTPImpairmentsRecoverToSpec(t *testing.T) {
	a := Generate(19)
	states, errs := rtp.Recover(a.RTPPackets, map[uint32]uint32{0x5EED: 90000})
	if len(errs) != 0 || len(states) != 1 {
		t.Fatalf("errs=%v states=%d", errs, len(states))
	}
	s := states[0]
	if s.Duplicates != 2 {
		t.Fatalf("duplicates=%d, want 2", s.Duplicates)
	}
	if s.Reordered != 3 {
		t.Fatalf("reordered=%d, want 3", s.Reordered)
	}
	if s.BurstLost != 4 || len(s.Gaps) != 1 || s.Gaps[0].Count != 4 {
		t.Fatalf("burst loss wrong: burst=%d gaps=%+v", s.BurstLost, s.Gaps)
	}
	// 40 generated - 4 lost = 36 unique ordered packets, nothing fabricated.
	if len(s.Ordered) != 36 {
		t.Fatalf("ordered=%d, want 36", len(s.Ordered))
	}
}

func TestArrivalGapsVaryContinuously(t *testing.T) {
	a := Generate(19)
	if len(a.ArrivalGaps) != len(a.RTPPackets) {
		t.Fatal("gap schedule must cover every arrival")
	}
	equalAdjacent := 0
	for i := 1; i < len(a.ArrivalGaps); i++ {
		if a.ArrivalGaps[i] == a.ArrivalGaps[i-1] {
			equalAdjacent++
		}
	}
	if equalAdjacent == len(a.ArrivalGaps)-1 {
		t.Fatal("arrival gaps never vary")
	}
}
