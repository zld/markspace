package manifest_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/diagnostic"
	"mediacore/internal/fixture"
	"mediacore/internal/manifest"
)

func TestDASHMultiRepresentationMappingAndOutOfRange(t *testing.T) {
	root := t.TempDir()
	b, err := fixture.GenerateAll(filepath.Join(root, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := manifest.ParseDASH(b.NormalDASH, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Entries) != 6 {
		t.Fatalf("entries=%d, want 6", len(d.Entries))
	}
	e, err := d.EntryAt("video", 1)
	if err != nil {
		t.Fatal(err)
	}
	if e.ProgramStartU != 40_000_000 || e.ProgramEndU != 80_000_000 {
		t.Fatalf("bad program interval [%d,%d)", e.ProgramStartU, e.ProgramEndU)
	}
	if !e.Aligned || e.DeviationU != 0 {
		t.Fatalf("normal video alignment: aligned=%v dev=%d", e.Aligned, e.DeviationU)
	}
	wide, err := d.EntryAt("wide", 0)
	if err != nil {
		t.Fatal(err)
	}
	if wide.Aligned || wide.DeviationU != 25_000_000 {
		t.Fatalf("wide alignment=%v dev=%d", wide.Aligned, wide.DeviationU)
	}

	_, err = d.EntryAt("video", 99)
	if !errors.Is(err, diagnostic.ErrOutOfRange) {
		t.Fatalf("out of range error=%v", err)
	}
	pos := diagnostic.PositionOf(err)
	if pos.Representation != "video" || pos.EntryIndex != 99 || pos.ManifestPath == "" {
		t.Fatalf("incomplete out-of-range location: %+v", pos)
	}
}

func TestDASHZeroTimescaleLocation(t *testing.T) {
	root := t.TempDir()
	b, err := fixture.GenerateAll(filepath.Join(root, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = manifest.ParseDASH(b.ZeroTimescale, root)
	if !errors.Is(err, diagnostic.ErrZeroTimescale) {
		t.Fatalf("zero timescale err=%v", err)
	}
	pos := diagnostic.PositionOf(err)
	if pos.Representation != "zero" || pos.EntryIndex != 0 {
		t.Fatalf("bad zero-timescale location: %+v", pos)
	}
}

func TestDASHMissingDurationLocation(t *testing.T) {
	root := t.TempDir()
	mpd := filepath.Join(root, "missing-duration.mpd")
	content := `<?xml version="1.0"?>
<MPD><Period><AdaptationSet><Representation id="nodur">
<SegmentTemplate timescale="1000" media="seg-$Number$.m4s">
<SegmentTimeline><S t="0"/></SegmentTimeline>
</SegmentTemplate></Representation></AdaptationSet></Period></MPD>`
	if err := os.WriteFile(mpd, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := manifest.ParseDASH(mpd, root)
	if !errors.Is(err, diagnostic.ErrMissingDuration) {
		t.Fatalf("missing duration err=%v", err)
	}
	pos := diagnostic.PositionOf(err)
	if pos.Representation != "nodur" || pos.EntryIndex != 0 {
		t.Fatalf("bad missing-duration location: %+v", pos)
	}
}
