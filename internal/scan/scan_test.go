package scan_test

import (
	"path/filepath"
	"testing"

	"archive/internal/model"
	"archive/internal/sample"
	"archive/internal/scan"
)

func genMP4(t *testing.T, opts sample.MP4Options) (string, *sample.Info) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "in.mp4")
	info, err := sample.GenMP4(p, opts)
	if err != nil {
		t.Fatal(err)
	}
	return p, info
}

func TestScanMP4Clean(t *testing.T) {
	p, _ := genMP4(t, sample.MP4Options{CorruptAt: -1, TruncateAt: -1, ColorConflict: true})
	res, err := scan.Scan(p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "mp4" {
		t.Fatalf("format = %q", res.Format)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(res.Tracks))
	}
	v := res.Tracks[0]
	if v.Type != "video" || !v.HasRotation || v.Rotation != 90 {
		t.Fatalf("video rotation = %+v", v)
	}
	if v.Crop == nil || v.Crop.Top != 8 || v.Crop.Left != 16 {
		t.Fatalf("crop = %+v", v.Crop)
	}
	if !v.ColorConflict || v.Color == nil || v.ColorAlt == nil {
		t.Fatalf("color conflict not preserved: %+v", v)
	}
	if v.Color.Primaries != 1 || v.ColorAlt.Primaries != 9 {
		t.Fatalf("color values rewritten: %+v / %+v", v.Color, v.ColorAlt)
	}
	if len(v.Samples) != 12 || len(res.Tracks[1].Samples) != 18 {
		t.Fatalf("sample counts = %d/%d", len(v.Samples), len(res.Tracks[1].Samples))
	}
	if got := res.DurationNs; got != 480_000_000 {
		t.Fatalf("duration = %d ns", got)
	}
}

func TestScanMP4DamageLocalisation(t *testing.T) {
	p, info := genMP4(t, sample.MP4Options{CorruptAt: 4096, TruncateAt: 8192})
	res, err := scan.Scan(p)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Errors.Damaged() {
		t.Fatal("damage not reported")
	}
	// Corruption must be localised within one media packet of byte 4096.
	var crcOff int64 = -1
	for _, e := range res.Errors {
		if e.Code == model.ErrEssenceCRC {
			crcOff = e.Offset
		}
	}
	if crcOff < 0 {
		t.Fatal("no ESSENCE_CRC error")
	}
	if d := 4096 - crcOff; d < 0 || d > info.PacketSizeAt(4096) {
		t.Fatalf("corrupt offset error %d exceeds one packet (%d)", d, info.PacketSizeAt(4096))
	}
	// Truncation must be localised within one media packet of byte 8192.
	var trOff int64 = -1
	for _, e := range res.Errors {
		if e.Code == model.ErrTruncated && e.Track >= 0 && (trOff < 0 || e.Offset < trOff) {
			trOff = e.Offset
		}
	}
	if trOff < 0 {
		t.Fatal("no sample-level TRUNCATED error")
	}
	if d := 8192 - trOff; d < 0 || d > info.PacketSizeAt(8192) {
		t.Fatalf("truncate offset error %d exceeds one packet (%d)", d, info.PacketSizeAt(8192))
	}
	// Bad samples are marked, intact ones stay recoverable.
	total, bad := res.SampleCount()
	if total != 30 || bad == 0 || bad == total {
		t.Fatalf("total=%d bad=%d", total, bad)
	}
}

func TestScanMXF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "in.mxf")
	if _, err := sample.GenMXF(p, sample.MXFOptions{CorruptAt: -1, TruncateAt: -1}); err != nil {
		t.Fatal(err)
	}
	res, err := scan.Scan(p) // dispatcher must sniff the KLV signature
	if err != nil {
		t.Fatal(err)
	}
	if res.Format != "mxf" {
		t.Fatalf("format = %q", res.Format)
	}
	if len(res.Partitions) != 2 || res.Partitions[0] != 0 {
		t.Fatalf("partitions = %v", res.Partitions)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(res.Tracks))
	}
	if res.Tracks[0].Type != "video" || res.Tracks[1].Type != "audio" {
		t.Fatalf("track types = %s/%s", res.Tracks[0].Type, res.Tracks[1].Type)
	}
	if len(res.Tracks[0].Samples) != 12 || len(res.Tracks[1].Samples) != 18 {
		t.Fatalf("sample counts = %d/%d", len(res.Tracks[0].Samples), len(res.Tracks[1].Samples))
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
}

func TestScanMXFDamage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "in.mxf")
	// Corrupt inside the third essence value so the KLV walk itself stays
	// intact and the damage must surface as an essence CRC failure.
	probe := filepath.Join(t.TempDir(), "probe.mxf")
	info, err := sample.GenMXF(probe, sample.MXFOptions{CorruptAt: -1, TruncateAt: -1})
	if err != nil {
		t.Fatal(err)
	}
	at := info.Samples[2].Offset + 10
	if _, err := sample.GenMXF(p, sample.MXFOptions{CorruptAt: at, TruncateAt: -1}); err != nil {
		t.Fatal(err)
	}
	res, err := scan.Scan(p)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Errors.Has(model.ErrEssenceCRC) {
		t.Fatalf("expected ESSENCE_CRC, got %v", res.Errors)
	}
	for _, e := range res.Errors {
		if e.Code == model.ErrEssenceCRC {
			if d := at - e.Offset; d < 0 || d > info.PacketSizeAt(at) {
				t.Fatalf("corrupt offset error %d exceeds one packet", d)
			}
		}
	}
}
