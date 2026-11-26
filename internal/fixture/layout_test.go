package fixture

import (
	"path/filepath"
	"testing"

	"liveregress/internal/fmp4"
)

func TestLayoutMatchesParsedSamples(t *testing.T) {
	dir := t.TempDir()
	tree, err := GenerateOne(dir, CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range tree.Layers {
		for _, rec := range tree.Segments[l.Name] {
			path := filepath.Join(tree.Root, filepath.FromSlash(rec.Path))
			r, err := fmp4.Open(path, rec.Path)
			if err != nil {
				t.Fatal(err)
			}
			seg, err := r.ParseSegment()
			if err != nil {
				t.Fatalf("%s: %v", rec.Path, err)
			}
			parsed := seg.Fragments[0].Samples
			if len(parsed) != len(rec.Layout) {
				t.Fatalf("%s: parsed %d samples, layout %d", rec.Path, len(parsed), len(rec.Layout))
			}
			for i := range parsed {
				if int64(parsed[i].Offset) != rec.Layout[i].Offset || parsed[i].Size != rec.Layout[i].Size {
					t.Fatalf("%s sample %d: parsed (%d,%d) layout (%d,%d)", rec.Path, i,
						parsed[i].Offset, parsed[i].Size, rec.Layout[i].Offset, rec.Layout[i].Size)
				}
			}
			r.Close()
		}
	}
}

func TestIFramePlaylistReferencesRealKeyframes(t *testing.T) {
	dir := t.TempDir()
	tree, err := GenerateOne(dir, CaseClean)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range tree.Layers {
		if l.Kind != "video" {
			continue
		}
		raw := tree.Segments[l.Name][0].Bytes
		for _, br := range tree.Segments[l.Name][0].KeyframeRanges {
			payload := raw[br.Offset : br.Offset+br.Length]
			// Generated keyframes are IDR (0x65) Annex B access units.
			found := false
			for i := 0; i+4 < len(payload); i++ {
				if payload[i] == 0 && payload[i+1] == 0 && payload[i+2] == 1 && payload[i+3] == 0x65 {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s range at %d does not contain an IDR", l.Name, br.Offset)
			}
		}
	}
}
