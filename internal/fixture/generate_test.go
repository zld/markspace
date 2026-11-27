package fixture_test

import (
	"testing"

	"mediasync/internal/audio"
	"mediasync/internal/fixture"
)

func TestGenerateDeterministic(t *testing.T) {
	a := fixture.Generate(17)
	b := fixture.Generate(17)
	if len(a.Tracks) != len(b.Tracks) || len(a.Tracks) == 0 {
		t.Fatalf("track count mismatch")
	}
	for i := range a.Tracks {
		ta, tb := a.Tracks[i], b.Tracks[i]
		if ta.Meta.TrackID != tb.Meta.TrackID || len(ta.Packets) != len(tb.Packets) {
			t.Fatalf("track %d differs", i)
		}
		for j := range ta.Packets {
			if string(ta.Packets[j].RTP) != string(tb.Packets[j].RTP) {
				t.Fatalf("track %s packet %d not reproducible", ta.Meta.TrackID, j)
			}
		}
	}
	// Different seed must change payload bytes somewhere.
	c := fixture.Generate(18)
	same := true
	for i := range a.Tracks {
		for j := range a.Tracks[i].Packets {
			if string(a.Tracks[i].Packets[j].RTP) != string(c.Tracks[i].Packets[j].RTP) {
				same = false
			}
		}
	}
	if same {
		t.Fatal("different seeds produced identical packets")
	}
}

func TestGeneratedScenarioHasExpectedDefects(t *testing.T) {
	sc := fixture.Generate(17)
	if _, ok := sc.Expect["sfx"]; !ok {
		t.Fatal("sfx markers missing")
	}
	if len(sc.Expect["sfx"].DuplicateOffsets) == 0 || len(sc.Expect["sfx"].GapSequences) == 0 {
		t.Fatalf("sfx markers incomplete: %+v", sc.Expect["sfx"])
	}
	if len(sc.Expect["broken"].TruncatedOffsets) != 1 ||
		sc.Expect["broken"].TruncatedOffsets[0] != 4 {
		t.Fatalf("broken marker: %+v", sc.Expect["broken"])
	}
	if len(sc.Expect["aac-rtp"].TruncatedOffsets) != 1 {
		t.Fatalf("aac marker: %+v", sc.Expect["aac-rtp"])
	}
}

func TestMinimalMP4RoundTrip(t *testing.T) {
	blob := fixture.BuildMinimalMP4(48000, 5)
	info, err := audio.ParseSampleTable("aac-mp4", blob)
	if err != nil {
		t.Fatal(err)
	}
	if info.Timescale != 48000 || len(info.Samples) != 5 {
		t.Fatalf("info=%+v", info)
	}
	for i, s := range info.Samples {
		// 1024/48000 s => 21,333,333 ns, exact integer.
		if s.DeltaNS != 21_333_333 {
			t.Fatalf("sample %d delta=%d", i, s.DeltaNS)
		}
		if s.Size != uint32(50+i) {
			t.Fatalf("sample %d size=%d", i, s.Size)
		}
	}
	if info.Color == nil || info.Color.ColorPrimaries != 9 ||
		info.Color.TransferCharacteristics != 16 || info.Color.MatrixCoefficients != 9 {
		t.Fatalf("color not parsed: %+v", info.Color)
	}
}
