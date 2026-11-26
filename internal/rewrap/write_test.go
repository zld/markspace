package rewrap_test

import (
	"os"
	"path/filepath"
	"testing"

	mp4scan "mediapipeline/internal/mp4"
	"mediapipeline/internal/rewrap"
)

func TestRewrapPreservesSamplesTimestampsAndSync(t *testing.T) {
	fx, err := mp4scan.BuildFixture(mp4scan.FixtureSpec{
		SampleCount: 10, SampleSize: 256, SyncNumbers: []uint32{1, 4, 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	if err := os.WriteFile(src, fx.Data, 0o600); err != nil {
		t.Fatal(err)
	}
	srcInfo, err := mp4scan.ScanFile(src, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if srcInfo.Report.Corrupt() {
		t.Fatalf("source corrupt: %+v", srcInfo.Report.Faults)
	}

	outBytes, err := rewrap.Rewrap(srcInfo)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "rewrapped.mp4")
	if err := os.WriteFile(dst, outBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	outInfo, err := mp4scan.ScanFile(dst, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if outInfo.Report.Corrupt() {
		t.Fatalf("rewrapped corrupt: %+v", outInfo.Report.Faults)
	}
	v := rewrap.Compare(srcInfo, outInfo)
	if v.Samples != 10 || v.FullSampleMatch != 10 {
		t.Fatalf("verification=%+v", v)
	}
	if v.RoundtripRate != 100 || v.KeyframeHitRate != 100 || v.SegmentHashRate != 100 {
		t.Fatalf("rates not 100: %+v", v)
	}
}
