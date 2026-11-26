package media_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/fixture"
	"mediad/internal/media"
)

func makePayloads(n, size int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		b := make([]byte, size)
		for j := range b {
			b[j] = byte((i*7 + j) & 0xff)
		}
		out[i] = b
	}
	return out
}

func TestParseMP4Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.mp4")
	spec := fixture.MP4Spec{
		Path:       path,
		Timescale:  1000, // 1 tick = 1 ms
		SampleDurs: []uint32{1000, 1000, 1000, 1000},
		Payload:    makePayloads(4, 32),
	}
	if err := fixture.WriteMP4(spec); err != nil {
		t.Fatal(err)
	}
	track, err := media.ParseMP4(path, "video")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(track.Samples) != 4 {
		t.Fatalf("samples = %d", len(track.Samples))
	}
	if track.Samples[0].DurationNs != media.NanoPerSecond/1000*1000 {
		t.Fatalf("first duration = %d", track.Samples[0].DurationNs)
	}
	if got := track.Samples[3].StartNs; got != 3*media.NanoPerSecond {
		t.Fatalf("last start = %d, want 3s", got)
	}
	// Offsets must advance by payload size within the single chunk.
	step := track.Samples[1].DataOffset - track.Samples[0].DataOffset
	if step != 32 {
		t.Fatalf("offset step = %d, want 32", step)
	}
}

func TestParseMP4Truncated(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.mp4")
	trunc := filepath.Join(dir, "trunc.mp4")
	spec := fixture.MP4Spec{
		Path:       valid,
		Timescale:  1000,
		SampleDurs: []uint32{1000, 1000, 1000, 1000},
		Payload:    makePayloads(4, 64),
	}
	if err := fixture.WriteTruncatedMP4(spec, trunc); err != nil {
		t.Fatal(err)
	}
	_, err := media.ParseMP4(trunc, "video")
	var me *media.Error
	if !errors.As(err, &me) {
		t.Fatalf("want structured error, got %v", err)
	}
	if me.Stage != media.StageScan || me.InputOffset < 0 {
		t.Fatalf("error not locatable: %+v", me)
	}
}

func TestParseMP4ZeroTimescale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zero.mp4")
	if err := fixture.WriteZeroTimescaleMP4(path, makePayloads(2, 16)); err != nil {
		t.Fatal(err)
	}
	_, err := media.ParseMP4(path, "video")
	var me *media.Error
	if !errors.As(err, &me) {
		t.Fatalf("want structured error, got %v", err)
	}
	if me.Stage != media.StageScan {
		t.Fatalf("stage = %s, want scan", me.Stage)
	}
}

func TestParseMP4MissingFile(t *testing.T) {
	if _, err := media.ParseMP4(filepath.Join(t.TempDir(), "nope.mp4"), "v"); err == nil {
		t.Fatal("missing file must error")
	}
}

func TestParseMP4RejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage.mp4")
	if err := os.WriteFile(path, []byte("not an mp4 at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := media.ParseMP4(path, "v")
	var me *media.Error
	if !errors.As(err, &me) || me.Stage != media.StageScan {
		t.Fatalf("garbage input must yield scan error, got %v", err)
	}
}
