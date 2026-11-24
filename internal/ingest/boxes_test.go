package ingest

import (
	"testing"

	"mediamerge/internal/fixgen"
	"mediamerge/internal/model"
)

func goodSpec() fixgen.FileSpec {
	return fixgen.FileSpec{
		Tracks: []fixgen.TrackSpec{
			{ID: 1, Handler: "vide", Timescale: 90000, Width: 1920, Height: 1080,
				Color: &fixgen.ColorSpec{Primaries: 1, Transfer: 1, Matrix: 1}},
			{ID: 2, Handler: "soun", Timescale: 48000, Channels: 2, SampleRate: 48000},
		},
		Frags: []fixgen.FragSpec{
			{Seq: 1, TrackID: 1, TFDT: 0, Samples: []fixgen.SampleSpec{
				{Data: []byte{1, 2, 3, 4}, Duration: 3000},
				{Data: []byte{5, 6}, Duration: 3000},
			}},
			{Seq: 1, TrackID: 2, TFDT: 0, Samples: []fixgen.SampleSpec{
				{Data: []byte{9, 9, 9}, Duration: 1024},
			}},
			{Seq: 2, TrackID: 1, TFDT: 6000, Samples: []fixgen.SampleSpec{
				{Data: []byte{7}, Duration: 3000},
			}},
		},
	}
}

func TestParseGoodFile(t *testing.T) {
	data := fixgen.Build(goodSpec())
	var errs model.Errors
	f, fatal := ParseFile("good.mp4", data, &errs)
	if fatal != nil {
		t.Fatalf("fatal: %v", fatal)
	}
	if errs.MaxLevel() >= model.LevelIsolated {
		t.Fatalf("unexpected diagnostics: %v", errs.All())
	}
	if len(f.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(f.Tracks))
	}
	v := f.Meta(1)
	if v == nil || v.Timescale != 90000 || v.Width != 1920 || v.Color == nil || v.Color.Primaries != 1 {
		t.Fatalf("video meta: %+v", v)
	}
	a := f.Meta(2)
	if a == nil || a.Channels != 2 || a.SampleRate != 48000 || a.Handler != "soun" {
		t.Fatalf("audio meta: %+v", a)
	}
	if len(f.Frags) != 3 {
		t.Fatalf("frags=%d", len(f.Frags))
	}
	fr := f.Frags[0]
	if fr.Seq != 1 || fr.TrackID != 1 || !fr.HasTFDT || fr.TFDT != 0 || len(fr.Samples) != 2 {
		t.Fatalf("frag0: %+v", fr)
	}
	// Sample payload must be sliceable at the recorded offsets.
	s0 := fr.Samples[0]
	if got := f.Data[s0.Offset : s0.Offset+int64(s0.Size)]; len(got) != 4 || got[0] != 1 {
		t.Fatalf("sample bytes mismatch: %v", got)
	}
	s1 := fr.Samples[1]
	if s1.Offset != s0.Offset+4 {
		t.Fatalf("sample offsets not contiguous: %d vs %d", s1.Offset, s0.Offset)
	}
}

func TestParseTruncatedFragmentIsolatesTrack(t *testing.T) {
	spec := goodSpec()
	full := fixgen.Build(spec)
	spec.TruncateAt = len(full) - 3 // cut inside the last mdat
	data := fixgen.Build(spec)
	var errs model.Errors
	f, fatal := ParseFile("trunc.mp4", data, &errs)
	if fatal != nil {
		t.Fatalf("should not be fatal: %v", fatal)
	}
	if errs.MaxLevel() != model.LevelIsolated {
		t.Fatalf("level=%v", errs.MaxLevel())
	}
	for _, e := range errs.All() {
		if e.ByteOffset < 0 {
			t.Fatalf("error lacks byte offset: %v", e)
		}
	}
	// Track 1 owned the truncated tail; its fragments are quarantined.
	for _, fr := range f.Frags {
		if fr.TrackID == 1 {
			t.Fatalf("isolated track fragments leaked: %+v", fr)
		}
	}
	if len(f.Frags) != 1 || f.Frags[0].TrackID != 2 {
		t.Fatalf("remaining frags: %+v", f.Frags)
	}
}

func TestParseTruncatedInitIsFatal(t *testing.T) {
	spec := goodSpec()
	spec.TruncateAt = 40 // inside moov
	data := fixgen.Build(spec)
	var errs model.Errors
	_, fatal := ParseFile("bad.mp4", data, &errs)
	if fatal == nil || fatal.Level != model.LevelFatal {
		t.Fatalf("expected fatal, got %v", fatal)
	}
	if fatal.ByteOffset < 0 {
		t.Fatal("fatal error lacks offset")
	}
}

func TestParseDeterministic(t *testing.T) {
	data := fixgen.Build(goodSpec())
	var e1, e2 model.Errors
	f1, _ := ParseFile("a.mp4", data, &e1)
	f2, _ := ParseFile("a.mp4", data, &e2)
	if len(f1.Frags) != len(f2.Frags) || len(e1.All()) != len(e2.All()) {
		t.Fatal("non-deterministic parse")
	}
	for i := range f1.Frags {
		if f1.Frags[i].Seq != f2.Frags[i].Seq || f1.Frags[i].MoofOffset != f2.Frags[i].MoofOffset {
			t.Fatal("fragment mismatch between runs")
		}
	}
}
