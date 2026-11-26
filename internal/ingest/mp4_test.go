package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"pwf/internal/fixture"
	"pwf/internal/media"
)

func TestParseMP4RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.mp4")
	sizes := []uint32{30, 30, 30, 30, 30}
	if err := fixture.WriteMP4File(path, 44100, 1024, sizes); err != nil {
		t.Fatal(err)
	}
	res, err := ParseMP4(path, "m1", CodecAAC)
	if err != nil {
		t.Fatal(err)
	}
	if res.Timescale != 44100 || len(res.Samples) != 5 {
		t.Fatalf("unexpected parse result: %+v", res)
	}
	if res.TotalTicks != 5*1024 {
		t.Fatalf("total ticks got %d want %d", res.TotalTicks, 5*1024)
	}
	// Sample offsets must be contiguous and monotonically increasing.
	for i := 1; i < len(res.Samples); i++ {
		want := res.Samples[i-1].Offset + int64(res.Samples[i-1].Size)
		if res.Samples[i].Offset != want {
			t.Fatalf("sample %d offset got %d want %d", i, res.Samples[i].Offset, want)
		}
	}
}

func TestParseMP4TruncatedBoxOffset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trunc.mp4")
	sizes := make([]uint32, 8)
	for i := range sizes {
		sizes[i] = 24
	}
	if err := fixture.WriteMP4File(path, 44100, 1024, sizes); err != nil {
		t.Fatal(err)
	}
	stszOff, err := fixture.TruncateMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseMP4(path, "trunc", CodecAAC)
	me, ok := media.AsError(err)
	if !ok {
		t.Fatalf("expected media.Error for truncation, got %v", err)
	}
	if me.Kind != media.KindTruncated {
		t.Fatalf("expected truncated kind, got %s", me.Kind)
	}
	if me.Offset != stszOff {
		t.Fatalf("truncation offset got %d want %d", me.Offset, stszOff)
	}
}

func TestParseMP4MissingFile(t *testing.T) {
	_, err := ParseMP4(filepath.Join(t.TempDir(), "nope.mp4"), "x", CodecAAC)
	if _, ok := media.AsError(err); !ok {
		t.Fatalf("missing file must be media.Error, got %v", err)
	}
}

func TestParseMP4RejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage.mp4")
	// Declare a huge box size that exceeds the file.
	if err := os.WriteFile(path, []byte{0x7f, 0xff, 0xff, 0xff, 'm', 'd', 'a', 't', 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseMP4(path, "g", CodecAAC)
	me, ok := media.AsError(err)
	if !ok {
		t.Fatalf("garbage must be media.Error, got %v", err)
	}
	if me.Offset < 0 {
		t.Fatalf("garbage error must carry an offset, got %+v", me)
	}
}
