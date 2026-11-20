package index

import (
	"bytes"
	"errors"
	"testing"

	"mp4seek/internal/bmff"
	"mp4seek/internal/fixture"
)

func readerFromFixture(t *testing.T, profile string) *bmff.Reader {
	t.Helper()
	var buf bytes.Buffer
	if err := fixture.Build(profile, &buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	return bmff.NewReader(bytes.NewReader(data), int64(len(data)))
}

func topBoxes(t *testing.T, r *bmff.Reader) []bmff.Box {
	t.Helper()
	var out []bmff.Box
	err := r.WalkTop(func(b bmff.Box) (bool, error) {
		out = append(out, b)
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func child(t *testing.T, r *bmff.Reader, parent bmff.Box, typ string) bmff.Box {
	t.Helper()
	kids, err := r.Children(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range kids {
		if k.Type == typ {
			return k
		}
	}
	t.Fatalf("no %s under %s", typ, parent.Path)
	return bmff.Box{}
}

func moovOf(t *testing.T, r *bmff.Reader) bmff.Box {
	t.Helper()
	for _, b := range topBoxes(t, r) {
		if b.Type == "moov" {
			return b
		}
	}
	t.Fatal("no moov")
	return bmff.Box{}
}

func stblOfTrak(t *testing.T, r *bmff.Reader, trak bmff.Box) bmff.Box {
	t.Helper()
	mdia := child(t, r, trak, "mdia")
	minf := child(t, r, mdia, "minf")
	return child(t, r, minf, "stbl")
}

func TestClassicEditWrapVideo(t *testing.T) {
	r := readerFromFixture(t, fixture.ProfileEditWrap)
	moov := moovOf(t, r)
	traks, err := r.Children(moov)
	if err != nil {
		t.Fatal(err)
	}
	var trakV bmff.Box
	n := 0
	for _, k := range traks {
		if k.Type == "trak" {
			n++
			if n == 1 {
				trakV = k
			}
		}
	}
	tbl, err := BuildClassic(r, stblOfTrak(t, r, trakV))
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Samples) != 12 {
		t.Fatalf("want 12 samples, got %d", len(tbl.Samples))
	}
	if tbl.Entry.Format != "avc1" || tbl.Entry.AvcC == nil {
		t.Fatalf("bad entry: %+v", tbl.Entry)
	}
	s1 := tbl.Samples[1]
	if s1.DTS != 10000 || s1.PTS != 90000 || !s1.Key {
		t.Fatalf("sample 1: %+v", s1)
	}
	if tbl.Samples[3].Key {
		t.Fatal("sample 3 must not be a sync sample")
	}
	if !tbl.Samples[6].Key {
		t.Fatal("sample 7 (1-based) must be sync")
	}
	// Decode-order offsets must be strictly increasing within the chunk.
	for i := 1; i < 12; i++ {
		if tbl.Samples[i].Offset <= tbl.Samples[i-1].Offset {
			t.Fatalf("offsets not increasing at %d", i)
		}
	}
}

func TestClassicEditWrapAudioNegativeCtts(t *testing.T) {
	r := readerFromFixture(t, fixture.ProfileEditWrap)
	moov := moovOf(t, r)
	kids, err := r.Children(moov)
	if err != nil {
		t.Fatal(err)
	}
	var trakA bmff.Box
	n := 0
	for _, k := range kids {
		if k.Type == "trak" {
			n++
			if n == 2 {
				trakA = k
			}
		}
	}
	tbl, err := BuildClassic(r, stblOfTrak(t, r, trakA))
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Samples) != 10 {
		t.Fatalf("want 10 audio samples, got %d", len(tbl.Samples))
	}
	if tbl.Samples[0].PTS != -1024 {
		t.Fatalf("audio sample 0 PTS = %d, want -1024", tbl.Samples[0].PTS)
	}
	if tbl.Samples[1].PTS != 0 {
		t.Fatalf("audio sample 1 PTS = %d, want 0", tbl.Samples[1].PTS)
	}
}

func TestClassicOOBRejected(t *testing.T) {
	r := readerFromFixture(t, fixture.ProfileOOB)
	moov := moovOf(t, r)
	kids, _ := r.Children(moov)
	for _, k := range kids {
		if k.Type != "trak" {
			continue
		}
		_, err := BuildClassic(r, stblOfTrak(t, r, k))
		if err != nil {
			if !errors.Is(err, bmff.ErrCorrupt) {
				t.Fatalf("want ErrCorrupt, got %v", err)
			}
			return
		}
	}
	t.Fatal("oob fixture accepted")
}

func TestTruncatedRejected(t *testing.T) {
	r := readerFromFixture(t, fixture.ProfileTruncated)
	err := r.WalkTop(func(b bmff.Box) (bool, error) { return false, nil })
	if !errors.Is(err, bmff.ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
}
