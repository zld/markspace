package fixture

import (
	"os"
	"testing"

	"mediad/internal/scan"
)

func TestRealGeneratedFMP4Scans(t *testing.T) {
	if !ffmpegPresent() {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()
	b, err := GenerateBase(LocalFFmpeg{}, dir, Options{
		DurationSec: 2, TargetKbps: 400, Width: 160, Height: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(b.MP4Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := scan.ScanMP4(f, b.MP4Path, "testhash")
	if err != nil {
		t.Fatalf("real fMP4 scan failed: %v", err)
	}
	if !res.Fragmented {
		t.Fatal("real fMP4 must be detected as fragmented")
	}
	if len(res.Tracks) == 0 {
		t.Fatal("real fMP4 must expose at least one track")
	}
	if len(res.Samples) == 0 {
		t.Fatalf("real fMP4 must yield fragment samples (got %d tracks)", len(res.Tracks))
	}
	tr := res.Tracks[0]
	if tr.TimeBase.Den <= 0 {
		t.Fatalf("track timescale invalid: %+v", tr)
	}
	t.Logf("scanned real fMP4: tracks=%d samples=%d timescale=%d codec=%s",
		len(res.Tracks), len(res.Samples), tr.TimeBase.Den, tr.Codec)
}
