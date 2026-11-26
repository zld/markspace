package mp4

import (
	"os"
	"path/filepath"
	"testing"

	"mediapipeline/pkg/media"
)

func writeFixture(t *testing.T, name string, spec FixtureSpec) (string, *BuiltFixture) {
	t.Helper()
	fx, err := BuildFixture(spec)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, fx.Data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, fx
}

func TestScanNormalFixtureTracksSamplesAndKeyframes(t *testing.T) {
	p, _ := writeFixture(t, "normal.mp4", FixtureSpec{
		SampleCount: 12, SampleSize: 128, SyncNumbers: []uint32{1, 5, 9},
	})
	info, err := ScanFile(p, 512)
	if err != nil {
		t.Fatal(err)
	}
	if info.Report.Corrupt() {
		t.Fatalf("unexpected corrupt faults: %+v", info.Report.Faults)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("tracks=%d", len(info.Tracks))
	}
	tr := info.Tracks[0]
	if tr.Timebase.Timescale != 3000 || tr.SampleCount != 12 || len(tr.Samples) != 12 {
		t.Fatalf("track metadata wrong: %+v", tr)
	}
	if len(tr.Keyframes) != 3 {
		t.Fatalf("keyframes=%+v", tr.Keyframes)
	}
	if !tr.Samples[0].Sync || tr.Samples[1].Sync {
		t.Fatalf("sync flags wrong: s0=%v s1=%v", tr.Samples[0].Sync, tr.Samples[1].Sync)
	}
	if tr.Samples[1].DTS != 3000 || tr.Samples[2].PTS != 6000 {
		t.Fatalf("timestamps wrong: dts1=%d pts2=%d", tr.Samples[1].DTS, tr.Samples[2].PTS)
	}
	if tr.TrackSHA256 == "" || len(tr.Segments) < 1 || tr.Segments[0].SHA256 == "" {
		t.Fatal("track and segment hashes must be populated")
	}
}

func TestScanReportsOutOfBoundsSTCOLocationAndTrack(t *testing.T) {
	p, fx := writeFixture(t, "bad-stco.mp4", FixtureSpec{
		SampleCount: 6, BadChunkOffset: true, SyncNumbers: []uint32{1},
	})
	info, err := ScanFile(p, 1024)
	if err != nil {
		t.Fatal(err)
	}
	faults := info.Report.ByClass(media.ClassBadSTCO)
	if len(faults) == 0 {
		t.Fatalf("expected stco fault, got %+v", info.Report.Faults)
	}
	f := faults[0]
	if f.TrackID != 1 || f.Offset != int64(fx.STCOEntriesStart+4) {
		t.Fatalf("fault location/track=%d/%d want %d", f.Offset, f.TrackID, fx.STCOEntriesStart+4)
	}
	if f.RecomputeScope != media.ScopeTrack {
		t.Fatalf("scope=%s", f.RecomputeScope)
	}
}

func TestScanReportsBackwardTimestamp(t *testing.T) {
	offsets := make([]int32, 8)
	offsets[0] = 6000
	offsets[1] = 0
	p, _ := writeFixture(t, "backward.mp4", FixtureSpec{
		SampleCount: 8, CompositionOffsets: offsets, SyncNumbers: []uint32{1},
	})
	info, err := ScanFile(p, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Report.HasClass(media.ClassBackwardPTS) {
		t.Fatalf("expected backward timestamp fault: %+v", info.Report.Faults)
	}
	f := info.Report.ByClass(media.ClassBackwardPTS)[0]
	if f.TrackID != 1 || f.RecomputeScope != media.ScopeSegment {
		t.Fatalf("fault=%+v", f)
	}
}

func TestScanReportsTruncatedMoov(t *testing.T) {
	_, fx := writeFixture(t, "full.mp4", FixtureSpec{SampleCount: 5})
	cut := fx.STCOEntriesStart + 4
	dir := t.TempDir()
	p := filepath.Join(dir, "truncated-moov.mp4")
	if err := os.WriteFile(p, fx.Data[:cut], 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := ScanFile(p, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Report.HasClass(media.ClassTruncatedMoov) {
		t.Fatalf("expected truncated moov fault: %+v", info.Report.Faults)
	}
	f := info.Report.ByClass(media.ClassTruncatedMoov)[0]
	if f.Offset < 0 || len(f.TrackIDs) == 0 {
		t.Fatalf("fault lacks location/tracks: %+v", f)
	}
}
