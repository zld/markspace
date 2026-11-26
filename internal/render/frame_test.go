package render

import (
	"bytes"
	"image/png"
	"testing"

	"continuity/internal/timeline"
)

func TestRenderPointIsDeterministicPNG(t *testing.T) {
	p := timeline.Point{Index: 0, PTSNS: 500_000_000, SyncErrorNS: 5_000_000,
		Video:  &timeline.TrackSample{Kind: "video", Source: "v.m4s"},
		Audio:  &timeline.TrackSample{Kind: "audio", Source: "a.m4s"},
		Switch: true, Sources: []string{"a.m4s", "v.m4s"}}
	f1, err := RenderPoint(p)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := RenderPoint(p)
	if err != nil {
		t.Fatal(err)
	}
	if f1.SHA256 != f2.SHA256 || !bytes.Equal(f1.PNGBytes, f2.PNGBytes) {
		t.Fatal("rendering is not deterministic")
	}
	im, err := png.Decode(bytes.NewReader(f1.PNGBytes))
	if err != nil {
		t.Fatal(err)
	}
	if im.Bounds().Dx() != Width || im.Bounds().Dy() != Height {
		t.Fatalf("bad size %v", im.Bounds())
	}
	if len(f1.SHA256) != 64 {
		t.Fatalf("bad hash %q", f1.SHA256)
	}
}

func TestRenderGapMarker(t *testing.T) {
	p := timeline.Point{Index: 1, PTSNS: 0, Gap: true, Missing: []string{"audio"},
		Video: &timeline.TrackSample{Kind: "video"}}
	f, err := RenderPoint(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.PNGBytes) == 0 || f.SHA256 == "" {
		t.Fatal("gap frame not rendered")
	}
}
