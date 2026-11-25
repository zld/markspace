package fixture

import (
	"bytes"
	"testing"
)

func twoTrackSpec() MediaSpec {
	return MediaSpec{
		Brand: "isom",
		Tracks: []TrackSpec{
			{TrackID: 1, Timescale: 48000, SampleCount: 12, SampleDelta: 480,
				PayloadSize: 16, SyncInterval: 4, HandlerType: "soun"},
			{TrackID: 2, Timescale: 24000, SampleCount: 12, SampleDelta: 240,
				PayloadSize: 20, SyncInterval: 4, HandlerType: "vide"},
		},
	}
}

func TestBuildAndVerifyRoundtrip(t *testing.T) {
	f := BuildFile(twoTrackSpec())
	if f.MdatData <= 0 || f.MdatOffset < 0 {
		t.Fatalf("bad mdat offsets: %+v", f)
	}
	for id, locs := range f.TrackMdat {
		if len(locs) != 12 {
			t.Fatalf("track %d: got %d samples, want 12", id, len(locs))
		}
		for i, loc := range locs {
			raw := f.Data[loc.Offset : loc.Offset+loc.Size]
			frame, ok := VerifyFrame(raw)
			if !ok {
				t.Fatalf("track %d sample %d failed verification", id, i)
			}
			if frame.TrackID != id || frame.Index != uint32(i) {
				t.Fatalf("frame id/index = %d/%d", frame.TrackID, frame.Index)
			}
		}
	}
}

func TestAllRequiredBoxesPresent(t *testing.T) {
	f := BuildFile(twoTrackSpec())
	for _, code := range []string{"ftyp", "moov", "mdat", "mdhd", "hdlr",
		"stts", "stsc", "stsz", "stco", "stss"} {
		if f.FindBoxHeader(code) < 0 {
			t.Fatalf("missing box %s", code)
		}
	}
	if off := f.FindBoxHeaderForTrack("stss", 1); off < 0 {
		t.Fatal("expected a second stss box for track 2")
	}
}

func TestCorruptSampleBreaksCrcOnly(t *testing.T) {
	f := BuildFile(twoTrackSpec())
	bad := f.CorruptSample(1, 0)
	loc := f.TrackMdat[1][0]
	if _, ok := VerifyFrame(bad[loc.Offset : loc.Offset+loc.Size]); ok {
		t.Fatal("corrupted sample still verifies")
	}
	// The following sample must remain intact.
	next := f.TrackMdat[1][1]
	if _, ok := VerifyFrame(bad[next.Offset : next.Offset+next.Size]); !ok {
		t.Fatal("neighbor sample damaged")
	}
}

func TestTruncationKeepsOriginalOffsets(t *testing.T) {
	f := BuildFile(twoTrackSpec())
	cut := f.TrackMdat[2][6].Offset
	trunc := f.TruncateMdat(len(f.Data) - cut)
	if len(trunc) != cut {
		t.Fatalf("truncated len = %d, want %d", len(trunc), cut)
	}
	// Bytes before the cut are byte-identical to the original.
	if !bytes.Equal(trunc, f.Data[:cut]) {
		t.Fatal("truncation shifted earlier bytes")
	}
}

func TestRemoveBoxMasksIndex(t *testing.T) {
	f := BuildFile(twoTrackSpec())
	off := f.FindBoxHeaderForTrack("stco", 0)
	out := f.RemoveBox(off)
	if string(out[off+4:off+8]) != "free" {
		t.Fatal("stco box was not masked")
	}
	if len(out) != len(f.Data) {
		t.Fatal("masking changed file length")
	}
}
