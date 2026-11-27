package fixture

import (
	"path/filepath"
	"testing"

	"continuity/media"
)

func TestGenerateValid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "valid.mp4")
	if err := Generate(p, ValidSpec()); err != nil {
		t.Fatal(err)
	}
	c, err := media.Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(c.Tracks))
	}
	v := c.VideoTrack()
	if v == nil || v.Timescale != 90000 || v.SampleCount != 180 {
		t.Fatalf("bad video track: %+v", v)
	}
	if !v.Color.Present || v.Color.Primaries != 1 {
		t.Fatalf("color metadata lost: %+v", v.Color)
	}
	if len(v.KeyframeNums) != 3 { // samples 1, 61, 121
		t.Fatalf("got %d keyframes, want 3", len(v.KeyframeNums))
	}
	a := c.Tracks[1]
	if a.ChannelCount != 2 || a.SampleRate != 48000 {
		t.Fatalf("bad audio params: ch=%d rate=%d", a.ChannelCount, a.SampleRate)
	}
	if len(c.FileSHA256) != 64 {
		t.Fatalf("file hash %q is not SHA-256 hex", c.FileSHA256)
	}
}

func TestGenerateTruncatedBox(t *testing.T) {
	p := filepath.Join(t.TempDir(), "corrupt.mp4")
	spec := ValidSpec()
	spec.TruncateMoov = true
	if err := Generate(p, spec); err != nil {
		t.Fatal(err)
	}
	_, err := media.Probe(p)
	if !media.IsKind(err, media.KindTruncatedBox) {
		t.Fatalf("want truncated-box error, got %v", err)
	}
	me, ok := err.(*media.Error)
	if !ok || !me.HasOffset {
		t.Fatalf("error must carry a byte offset: %v", err)
	}
}

func TestGenerateTimeRegression(t *testing.T) {
	p := filepath.Join(t.TempDir(), "regress.mp4")
	spec := ValidSpec()
	spec.Tracks[0].ZeroDeltaAt = 10
	if err := Generate(p, spec); err != nil {
		t.Fatal(err)
	}
	c, err := media.Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := media.BuildIndex(c.VideoTrack()); !media.IsKind(err, media.KindTimeRegression) {
		t.Fatalf("want time-regression error, got %v", err)
	}
}
