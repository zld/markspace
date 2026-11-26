package fixture

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/adapter"
	"continuity/internal/core"
	"continuity/internal/manifest"
)

func TestGenerateNormal(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir, "normal", 17); err != nil {
		t.Fatal(err)
	}
	pl, err := manifest.ParseHLS(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Segments) != 6 {
		t.Fatalf("segments=%d, want 6", len(pl.Segments))
	}
	p := adapter.MP4Parser{}
	infos := map[string]*core.SegmentInfo{}
	for _, seg := range pl.Segments {
		f, err := os.Open(filepath.Join(dir, seg.URI))
		if err != nil {
			t.Fatal(err)
		}
		info, err := p.ParseSegment(f, seg.URI)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", seg.URI, err)
		}
		infos[seg.URI] = info
	}
	// duplicate copy shares the content fingerprint of seg2
	if infos["seg2.m4s"].Fingerprint != infos["seg2b.m4s"].Fingerprint {
		t.Fatal("seg2b fingerprint differs from seg2")
	}
	// DASH manifest lists the same entries
	dl, err := manifest.ParseDASH(filepath.Join(dir, "index.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dl.Segments) != len(pl.Segments) {
		t.Fatalf("dash segments=%d", len(dl.Segments))
	}
	// arrivals and events exist
	if _, err := os.Stat(filepath.Join(dir, "arrivals.tsv")); err != nil {
		t.Fatal(err)
	}
	evs, err := readEvents(t, dir)
	if err != nil || len(evs) == 0 {
		t.Fatalf("events: %v %d", err, len(evs))
	}
}

func TestGenerateDamaged(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir, "damaged", 17); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "seg2.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = (adapter.MP4Parser{}).ParseSegment(bytes.NewReader(data), "seg2.m4s")
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Code != core.CodeTruncatedBox || ce.Offset != 24 {
		t.Fatalf("want truncated_box at 24, got %v", err)
	}
}

func TestGenerateDeterministic(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	if err := Generate(d1, "recover", 17); err != nil {
		t.Fatal(err)
	}
	if err := Generate(d2, "recover", 17); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.m3u8", "index.mpd", "arrivals.tsv", "events.rtp", "seg0.m4s", "seg4.m4s"} {
		a, err := os.ReadFile(filepath.Join(d1, name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(d2, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%s differs between runs", name)
		}
	}
}

func readEvents(t *testing.T, dir string) ([]core.Event, error) {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "events.rtp"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return (adapter.RTPParser{}).ParseEvents(f, "events.rtp")
}
