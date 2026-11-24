package ebml_test

import (
	"bytes"
	"testing"

	"mediacheck/ebml"
	"mediacheck/internal/fixture"
	"mediacheck/timeline"
)

func scanFixture(t *testing.T, cfg fixture.Config) (*ebml.ScanResult, []byte, error) {
	t.Helper()
	stream, err := fixture.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ebml.Scan(bytes.NewReader(stream))
	return res, stream, err
}

func TestScanHealthyStream(t *testing.T) {
	res, _, err := scanFixture(t, fixture.Config{Seed: 17, Frames: 12})
	if err != nil {
		t.Fatalf("healthy scan failed: %v", err)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(res.Tracks))
	}
	if res.BlockCount != 24 { // one video + one audio block per cluster
		t.Fatalf("want 24 blocks, got %d", res.BlockCount)
	}
	if len(res.CheckFailures) != 0 {
		t.Fatalf("unexpected CRC failures: %v", res.CheckFailures)
	}
	var v, a *ebml.TrackSpec
	for _, tr := range res.Tracks {
		if tr.IsVideo() {
			v = tr
		}
		if tr.IsAudio() {
			a = tr
		}
	}
	if v == nil || a == nil || v.Width != 48 || v.Height != 27 || a.SampleRate != 48000 {
		t.Fatalf("track specs wrong: %+v %+v", v, a)
	}
	// Monotonic per-track timestamps at 24 fps (~41.667 ms spacing).
	var prev int64 = -1
	videoCount := 0
	for _, b := range res.Blocks {
		if b.TrackNumber != fixture.VideoTrackNumber {
			continue
		}
		if b.TimeNs <= prev {
			t.Fatalf("non-monotonic video ts: %d after %d", b.TimeNs, prev)
		}
		prev = b.TimeNs
		videoCount++
	}
	if videoCount != 12 {
		t.Fatalf("want 12 video blocks, got %d", videoCount)
	}
}

func TestScanTruncatedClusterKeepsEarlierIndex(t *testing.T) {
	stream, err := fixture.Build(fixture.Config{Seed: 17, Frames: 12})
	if err != nil {
		t.Fatal(err)
	}
	cut, off, err := fixture.TruncateAtCluster(stream, 6)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(cut)) != off {
		t.Fatalf("cut length mismatch %d vs %d", len(cut), off)
	}
	res, scanErr := ebml.Scan(bytes.NewReader(cut))
	if !timeline.IsCategory(scanErr, timeline.CatCorrupt) {
		t.Fatalf("want corrupt error, got %v", scanErr)
	}
	if res.TruncatedAt < 0 {
		t.Fatal("truncation offset not recorded")
	}
	// Clusters 0..5 fully parsed: 6 video + 6 audio blocks retained.
	if res.BlockCount != 12 {
		t.Fatalf("want 12 retained blocks, got %d", res.BlockCount)
	}
}

func TestScanCRCFailureRecorded(t *testing.T) {
	stream, err := fixture.Build(fixture.Config{Seed: 17, Frames: 8})
	if err != nil {
		t.Fatal(err)
	}
	bad, off, err := fixture.FlipClusterPayload(stream, 3)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ebml.Scan(bytes.NewReader(bad))
	if err != nil {
		t.Fatalf("CRC mismatch must not fail framing: %v", err)
	}
	if len(res.CheckFailures) != 1 || res.CheckFailures[0] <= 0 {
		t.Fatalf("want one CRC failure, got %v", res.CheckFailures)
	}
	for _, b := range res.Blocks {
		if b.ClusterOff == res.CheckFailures[0] && b.CheckOK {
			t.Fatalf("block at offset %d should be marked check-failed", off)
		}
	}
}

func TestScanLiveSegment(t *testing.T) {
	res, _, err := scanFixture(t, fixture.Config{Seed: 3, Frames: 6, LiveSegment: true})
	if err != nil {
		t.Fatalf("live segment scan failed: %v", err)
	}
	if !res.SegmentLive || res.BlockCount != 12 {
		t.Fatalf("live scan wrong: live=%v blocks=%d", res.SegmentLive, res.BlockCount)
	}
}

func TestScanColorConflictAndRawEnum(t *testing.T) {
	res, _, err := scanFixture(t, fixture.Config{
		Seed: 7, Frames: 4, ColorConflict: true, UnknownColor: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ContainerColor == nil || res.ContainerColor.Matrix != 6 {
		t.Fatal("container colour record missing")
	}
	for _, tr := range res.Tracks {
		if tr.IsVideo() {
			if tr.Color == nil || tr.Color.Matrix != 9 || tr.Color.RawEnum != 0x2A2A {
				t.Fatalf("track colour/raw enum wrong: %+v", tr.Color)
			}
		}
	}
}
