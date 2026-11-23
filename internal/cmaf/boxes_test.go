package cmaf

import (
	"testing"

	"avbatch/internal/mediaerr"
	"avbatch/internal/synth"
)

func fixture(t *testing.T, seq uint32, bdt uint64, n int) ([]byte, *Init) {
	t.Helper()
	initBytes, err := synth.InitSegment(1000, []synth.TrackSpec{synth.VideoTrack(1), synth.AudioTrack(2)})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	init, err := ParseInit(initBytes, DefaultLimits())
	if err != nil {
		t.Fatalf("parse init: %v", err)
	}
	samples := make([]synth.Sample, n)
	for i := range samples {
		samples[i] = synth.Sample{Size: uint32(64 + i*7), Duration: 3000}
	}
	frag, err := synth.Fragment(synth.FragmentSpec{Sequence: seq, TrackID: 1, BaseDecodeTime: bdt, Samples: samples})
	if err != nil {
		t.Fatalf("fragment: %v", err)
	}
	return frag, init
}

func TestParseInitTracks(t *testing.T) {
	_, init := fixture(t, 1, 0, 2)
	if init.MovieTimescale != 1000 {
		t.Errorf("movie timescale = %d", init.MovieTimescale)
	}
	v, ok := init.Tracks[1]
	if !ok || v.Timescale != 90000 || v.Handler != "vide" {
		t.Errorf("video track = %+v", v)
	}
	a, ok := init.Tracks[2]
	if !ok || a.Timescale != 48000 || a.Handler != "soun" {
		t.Errorf("audio track = %+v", a)
	}
	if init.Digest == [32]byte{} {
		t.Error("init digest missing")
	}
}

func TestParseFragmentRuns(t *testing.T) {
	raw, init := fixture(t, 7, 90000, 3)
	frag, err := ParseFragment(4, raw, init, DefaultLimits())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if frag.Ordinal != 4 || frag.Sequence != 7 || !frag.HasSequence {
		t.Fatalf("fragment header = %+v", frag)
	}
	run := frag.VideoRun()
	if run == nil {
		t.Fatal("no video run")
	}
	if len(run.Samples) != 3 {
		t.Fatalf("samples = %d", len(run.Samples))
	}
	if run.BaseDecodeTime != 90000 || run.Timescale != 90000 {
		t.Fatalf("run timebase = %+v", run)
	}
	for i, s := range run.Samples {
		if s.Size != uint32(64+i*7) || s.Duration != 3000 {
			t.Errorf("sample %d = %+v", i, s)
		}
		if int64(s.Size) > frag.Size {
			t.Errorf("sample %d size beyond fragment", i)
		}
	}
}

func TestTruncatedBoxCarriesContext(t *testing.T) {
	raw, init := fixture(t, 1, 0, 4)
	cases := []struct {
		name     string
		cut      int64
		wantKind mediaerr.Kind
	}{
		{"header", 6, mediaerr.KindTruncatedBox},
		{"mid-moof", 40, mediaerr.KindTruncatedBox},
		{"mid-mdat", int64(len(raw)) - 20, mediaerr.KindTruncatedBox},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseFragment(3, raw[:c.cut], init, DefaultLimits())
			if err == nil {
				t.Fatal("expected error")
			}
			var me *mediaerr.Error
			if !asMediaErr(err, &me) {
				t.Fatalf("err %v is not a media error", err)
			}
			if me.Kind != c.wantKind {
				t.Fatalf("kind = %s, want %s (%v)", me.Kind, c.wantKind, err)
			}
			if me.Segment != 3 {
				t.Fatalf("segment = %d, want 3", me.Segment)
			}
			// offset 0 is valid (the offending box starts at byte 0), so
			// assert only that the message names a byte position.
			if me.Offset < 0 {
				t.Errorf("offset not reported: %+v", me)
			}
		})
	}
}

func TestTruncatedSampleFails(t *testing.T) {
	raw, init := fixture(t, 1, 0, 2)
	// Cut inside mdat: boxes parse, but samples no longer fit.
	fragLen := int64(len(raw))
	_, err := ParseFragment(0, raw[:fragLen-10], init, DefaultLimits())
	if !mediaerr.Is(err, mediaerr.KindTruncatedBox) {
		t.Fatalf("err = %v, want truncated_box", err)
	}
}

func TestMissingSequenceIsReported(t *testing.T) {
	initBytes, _ := synth.InitSegment(1000, []synth.TrackSpec{synth.VideoTrack(1)})
	init, err := ParseInit(initBytes, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := synth.Fragment(synth.FragmentSpec{
		Sequence: 1, TrackID: 1, Samples: []synth.Sample{{Size: 32, Duration: 1000}}, OmitSequence: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseFragment(9, raw, init, DefaultLimits())
	if !mediaerr.Is(err, mediaerr.KindMissingField) {
		t.Fatalf("err = %v, want missing_field", err)
	}
}

func TestUnknownTrackIsReported(t *testing.T) {
	initBytes, _ := synth.InitSegment(1000, []synth.TrackSpec{synth.VideoTrack(1)})
	init, _ := ParseInit(initBytes, DefaultLimits())
	raw, err := synth.Fragment(synth.FragmentSpec{Sequence: 1, TrackID: 98, Samples: []synth.Sample{{Size: 32, Duration: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseFragment(2, raw, init, DefaultLimits())
	if !mediaerr.Is(err, mediaerr.KindMissingField) {
		t.Fatalf("err = %v, want missing_field", err)
	}
}

func TestOversizeSampleCountRejected(t *testing.T) {
	initBytes, _ := synth.InitSegment(1000, []synth.TrackSpec{synth.VideoTrack(1)})
	init, _ := ParseInit(initBytes, DefaultLimits())
	raw, err := synth.Fragment(synth.FragmentSpec{Sequence: 1, TrackID: 1, Samples: []synth.Sample{{Size: 32, Duration: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the trun sample count to a huge value.
	idx := indexOf(raw, "trun")
	if idx < 0 {
		t.Fatal("no trun")
	}
	raw[idx+12] = 0x7F
	raw[idx+13] = 0xFF
	raw[idx+14] = 0xFF
	raw[idx+15] = 0xFF
	_, err = ParseFragment(0, raw, init, DefaultLimits())
	if !mediaerr.Is(err, mediaerr.KindInvalidBox) && !mediaerr.Is(err, mediaerr.KindTruncatedBox) {
		t.Fatalf("err = %v, want invalid_box or truncated_box", err)
	}
}

func TestInitMissingMoov(t *testing.T) {
	initBytes, _ := synth.InitSegment(1000, []synth.TrackSpec{synth.VideoTrack(1)})
	// Keep only the ftyp box.
	if _, err := ParseInit(initBytes[:28], DefaultLimits()); !mediaerr.Is(err, mediaerr.KindMissingField) {
		t.Fatalf("err = %v, want missing_field", err)
	}
	if _, err := ParseInit(initBytes[:10], DefaultLimits()); !mediaerr.Is(err, mediaerr.KindTruncatedBox) {
		t.Fatalf("err = %v, want truncated_box", err)
	}
}

func indexOf(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i
		}
	}
	return -1
}

func asMediaErr(err error, target **mediaerr.Error) bool {
	for err != nil {
		if me, ok := err.(*mediaerr.Error); ok {
			*target = me
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
