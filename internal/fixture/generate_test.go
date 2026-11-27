package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abema/go-mp4"

	"mediabatch/internal/scan"
)

func TestGeneratedInitAndFragmentAreReadable(t *testing.T) {
	dir := t.TempDir()
	initData, err := BuildInit(1000, 1, "iso6")
	if err != nil {
		t.Fatal(err)
	}
	initPath := filepath.Join(dir, "init.mp4")
	if err := os.WriteFile(initPath, initData, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(initPath)
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := mp4.ExtractBoxesWithPayload(f, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()},
	})
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 2 {
		t.Fatalf("init boxes=%d", len(boxes))
	}
	var mdhd *mp4.Mdhd
	var tkhd *mp4.Tkhd
	for _, b := range boxes {
		switch v := b.Payload.(type) {
		case *mp4.Mdhd:
			mdhd = v
		case *mp4.Tkhd:
			tkhd = v
		}
	}
	if mdhd == nil || mdhd.Timescale != 1000 {
		t.Fatalf("mdhd=%+v", mdhd)
	}
	if tkhd == nil || tkhd.TrackID != 1 {
		t.Fatalf("tkhd=%+v", tkhd)
	}

	payload := deterministicPayload("x", 100)
	frag, err := BuildFragment(fragmentSpec{
		Sequence: 1, TrackID: 1, BaseDecode: 0, Timescale: 1000, Payload: payload,
		Samples: []sampleSpec{{Duration: 2000, Size: 50, Sync: true}, {Duration: 2000, Size: 50}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fragPath := filepath.Join(dir, "seg.m4s")
	if err := os.WriteFile(fragPath, frag, 0o644); err != nil {
		t.Fatal(err)
	}
	res := scan.Scan(scan.Request{
		Manifest: "m.mpd", Representation: "v", Ref: "seg.m4s", Path: fragPath,
		InitPath: initPath,
	})
	if !res.OK() {
		t.Fatalf("status=%s issues=%+v", res.Status, res.Issues)
	}
	if res.Info.SampleCount != 2 || res.Info.PayloadBytes != 100 {
		t.Fatalf("info=%+v", res.Info)
	}
	if res.Info.Timescale != 1000 || res.Info.TrackID != 1 {
		t.Fatalf("info=%+v", res.Info)
	}
	if !res.Info.Keyframe {
		t.Fatal("first sample should be a keyframe")
	}

	// Truncating the fragment must be detected.
	if err := os.WriteFile(fragPath, frag[:len(frag)-10], 0o644); err != nil {
		t.Fatal(err)
	}
	bad := scan.Scan(scan.Request{Manifest: "m.mpd", Ref: "seg.m4s", Path: fragPath, InitPath: initPath})
	if bad.OK() {
		t.Fatal("truncated fragment must fail")
	}
	if bad.Status != scan.StatusMalformed && bad.Status != scan.StatusTruncated {
		t.Fatalf("status=%s", bad.Status)
	}
}

func TestGenerateTree(t *testing.T) {
	work := t.TempDir()
	layout, err := Generate(work, Options{SegmentCount: 12, Chunk: 3})
	if err != nil {
		t.Fatal(err)
	}
	if layout.Segments != 24 {
		t.Fatalf("segments=%d", layout.Segments)
	}
	for _, rel := range []string{"dash/manifest.mpd", "hls/prog.m3u8", "dash/init.mp4", "expectations.json"} {
		if _, err := os.Stat(filepath.Join(layout.InputDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	exp, err := LoadExpectations(filepath.Join(layout.InputDir, "expectations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(exp) != 36 {
		t.Fatalf("expectations=%d", len(exp))
	}
}
