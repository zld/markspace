package fixture

import (
	"path/filepath"
	"testing"

	"continuity/internal/manifest"
)

func TestGenerateOKTree(t *testing.T) {
	dir := t.TempDir()
	g, err := New(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Generate(); err != nil {
		t.Fatal(err)
	}

	// Primary DASH parses and has one gap per representation (7 segments).
	prim, err := manifest.ParseDASH(filepath.Join(g.Root, "dash", "primary", "prog.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(prim.Representations) != 3 {
		t.Fatalf("representations = %d", len(prim.Representations))
	}
	for _, rep := range prim.Representations {
		if len(rep.Segments) != segmentsPerRep-1 {
			t.Errorf("rep %s primary segments = %d, want %d",
				rep.ID, len(rep.Segments), segmentsPerRep-1)
		}
	}
	// Backup has full continuity (8 segments) covering the gap.
	back, err := manifest.ParseDASH(filepath.Join(g.Root, "dash", "backup", "prog.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rep := range back.Representations {
		if len(rep.Segments) != segmentsPerRep {
			t.Errorf("backup rep %s segments = %d", rep.ID, len(rep.Segments))
		}
	}

	// HLS playlist parses and records a discontinuity.
	hls, err := manifest.ParseHLS(filepath.Join(g.Root, "hls", "backup", "pl.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hls.Representations[0].Discontinuities) != 1 {
		t.Errorf("discontinuities = %v", hls.Representations[0].Discontinuities)
	}
}

func TestGenerateBadCases(t *testing.T) {
	cases := []string{CaseTruncatedMdat, CaseDuplicateDASH, CaseHLSGap, CaseRTPWrap}
	for _, kind := range cases {
		dir := t.TempDir()
		g, err := New(filepath.Join(dir, kind))
		if err != nil {
			t.Fatal(err)
		}
		if err := g.GenerateBad(kind); err != nil {
			t.Fatalf("case %s: %v", kind, err)
		}
	}
}

func TestUnknownBadCase(t *testing.T) {
	g, _ := New(t.TempDir())
	if err := g.GenerateBad("nope"); err == nil {
		t.Fatal("expected error for unknown case")
	}
}
