package mp4

import (
	"bytes"
	"testing"

	"media.rebuild/internal/fixtures"
	"media.rebuild/internal/model"
)

func index(t *testing.T, raw []byte) *Index {
	t.Helper()
	idx, err := Read(bytes.NewReader(raw), "fixture.mp4", int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestReadHealthyTrack(t *testing.T) {
	idx := index(t, fixtures.DefaultMP4().Bytes())
	if len(idx.Tracks) != 1 {
		t.Fatalf("tracks %d", len(idx.Tracks))
	}
	tr := idx.Tracks[0]
	if !tr.Valid || len(tr.Samples) != 5 || len(tr.Errors) != 0 {
		t.Fatalf("track %+v", tr)
	}
	for i, s := range tr.Samples {
		if s.Index != i || s.TrackID != tr.TrackID {
			t.Fatalf("sample %d metadata %+v", i, s)
		}
		if s.TimeNs != int64(i)*1_000_000_000 {
			t.Fatalf("sample %d time %d", i, s.TimeNs)
		}
		if s.DurationNs != 1_000_000_000 {
			t.Fatalf("sample %d duration %d", i, s.DurationNs)
		}
		if s.CTS != s.DTS {
			t.Fatalf("sample %d cts %d dts %d", i, s.CTS, s.DTS)
		}
	}
	if idx.ValidTracks() != 1 || idx.InvalidSamples() != 0 {
		t.Fatalf("valid %d invalid samples %d", idx.ValidTracks(), idx.InvalidSamples())
	}
}

func TestOversizeSampleMarksTrackInvalid(t *testing.T) {
	m := fixtures.DefaultMP4()
	m.OversizeSample = 2
	idx := index(t, m.Bytes())
	tr := idx.Tracks[0]
	if tr.Valid {
		t.Fatal("track with oversize sample marked valid")
	}
	if len(tr.Samples) != 0 {
		t.Fatalf("invalid track published %d samples", len(tr.Samples))
	}
	if len(tr.Errors) == 0 || !model.IsCode(tr.Errors[0], model.CodeMP4Sample) {
		t.Fatalf("errors %v", tr.Errors)
	}
	if idx.InvalidSamples() == 0 {
		t.Fatal("invalid sample not counted")
	}
	// The offending range is anchored at a real byte offset.
	if tr.Errors[0].Source.Offset <= 0 {
		t.Fatalf("offset %d", tr.Errors[0].Source.Offset)
	}
}

func TestNonIncreasingDTSMarksTrackInvalid(t *testing.T) {
	m := fixtures.DefaultMP4()
	m.ZeroDeltaAfter = 3
	idx := index(t, m.Bytes())
	tr := idx.Tracks[0]
	if tr.Valid {
		t.Fatal("track with frozen DTS marked valid")
	}
	if len(tr.Samples) != 0 {
		t.Fatalf("samples leaked: %d", len(tr.Samples))
	}
	found := false
	for _, e := range tr.Errors {
		if e.Code == model.CodeMP4Sample {
			found = true
		}
	}
	if !found {
		t.Fatalf("no sample error: %v", tr.Errors)
	}
}

func TestNonMP4File(t *testing.T) {
	idx := index(t, []byte("this is not an mp4 file at all"))
	if len(idx.Errors) == 0 {
		t.Fatal("garbage accepted")
	}
	if idx.Errors[0].Code != model.CodeMP4Container {
		t.Fatalf("code %s", idx.Errors[0].Code)
	}
	if len(idx.Tracks) != 0 {
		t.Fatal("fabricated tracks")
	}
}

func TestTruncatedAfterFtyp(t *testing.T) {
	raw := fixtures.DefaultMP4().Bytes()[:16]
	idx := index(t, raw)
	if len(idx.Errors) == 0 || len(idx.Tracks) != 0 {
		t.Fatalf("truncated file: tracks %d errors %v", len(idx.Tracks), idx.Errors)
	}
}

func TestTinyFile(t *testing.T) {
	idx := index(t, []byte("ab"))
	if len(idx.Errors) != 1 || idx.Errors[0].Code != model.CodeMP4Container {
		t.Fatalf("errors %v", idx.Errors)
	}
}

func TestRescale(t *testing.T) {
	cases := []struct {
		ticks int64
		scale uint32
		want  int64
	}{
		{0, 12800, 0},
		{512, 12800, 40_000_000},
		{12800, 12800, 1_000_000_000},
		{90_000, 90_000, 1_000_000_000},
		{1, 3, 333_333_333},
		{1, 0, 0},
	}
	for _, c := range cases {
		if got := rescale(c.ticks, c.scale); got != c.want {
			t.Errorf("rescale(%d,%d) = %d want %d", c.ticks, c.scale, got, c.want)
		}
	}
}

func TestOpenMissingFile(t *testing.T) {
	if _, err := Open("/nonexistent/review.mp4"); !model.IsCode(err, model.CodeIO) {
		t.Fatalf("error %v", err)
	}
}

func TestKeyframeTableAbsentKeepsAllSamples(t *testing.T) {
	// The generator writes no stss box; every sample is then a sync point.
	idx := index(t, fixtures.DefaultMP4().Bytes())
	for _, s := range idx.Tracks[0].Samples {
		if s.Keyframe {
			t.Fatalf("sample %d marked keyframe without a stss table", s.Index)
		}
	}
}

func TestIndexIsDeterministic(t *testing.T) {
	raw := fixtures.DefaultMP4().Bytes()
	a := index(t, raw)
	b := index(t, raw)
	if len(a.Tracks) != len(b.Tracks) {
		t.Fatal("track count differs")
	}
	for i := range a.Tracks {
		if a.Tracks[i].TrackID != b.Tracks[i].TrackID || len(a.Tracks[i].Samples) != len(b.Tracks[i].Samples) {
			t.Fatalf("track %d differs", i)
		}
		for j := range a.Tracks[i].Samples {
			if a.Tracks[i].Samples[j] != b.Tracks[i].Samples[j] {
				t.Fatalf("sample %d/%d differs", i, j)
			}
		}
	}
}
