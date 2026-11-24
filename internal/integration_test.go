package medpipe

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"
)

// TestIntegrationFullBatch runs the generated fixture batch end to end and
// asserts the acceptance-level invariants: no fatal track on the main batch,
// sub-5% bitrate delta and sub-1 ms alignment error.
func TestIntegrationFullBatch(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	specs, fatalSpecs, err := GenerateFixtures(dir, DefaultFixtureParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 7 || len(fatalSpecs) != 1 {
		t.Fatalf("fixture spec counts wrong: %d %d", len(specs), len(fatalSpecs))
	}
	opts := PipelineOptions{InputDir: dir, WorkDir: work, ChunkSize: 12, TargetBPS: 4_000_000, Resume: true}
	m, err := RunBatch(specs, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Success {
		t.Fatalf("main batch must succeed (recoverable anomalies allowed): %+v", m.Errors)
	}
	absDelta := m.Metrics.BitrateDeltaPct
	if absDelta < 0 {
		absDelta = -absDelta
	}
	if absDelta > 5.0 {
		t.Fatalf("bitrate delta %.3f%% exceeds 5%%", m.Metrics.BitrateDeltaPct)
	}
	if m.Metrics.ManifestErrorMS > 1.0 {
		t.Fatalf("manifest alignment error %.3f ms exceeds 1 ms", m.Metrics.ManifestErrorMS)
	}
	// Required headline metrics are all populated.
	if m.Metrics.TargetBPS == 0 || m.Metrics.ActualBPS == 0 || m.Metrics.QualityMean == 0 {
		t.Fatalf("headline metrics missing: %+v", m.Metrics)
	}
	// The truncated and conflict tracks are degraded but not failed.
	for _, tr := range m.Tracks {
		if tr.Status == StatusFailed {
			t.Fatalf("track %s failed in the main batch", tr.Track)
		}
	}
}

// TestIntegrationFatalTrackFailsBatch verifies the unrepairable fixture track
// produces an offset-anchored fatal error and fails its batch.
func TestIntegrationFatalTrackFailsBatch(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := GenerateFixtures(dir, DefaultFixtureParams()); err != nil {
		t.Fatal(err)
	}
	fatalDir := filepath.Join(dir, DefaultFixtureNames().FatalDir)
	specs, err := LoadSpecs(fatalDir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := RunBatch(specs, PipelineOptions{
		InputDir: fatalDir, WorkDir: filepath.Join(dir, "work-fatal"), ChunkSize: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Success {
		t.Fatal("fatal batch must not be marked successful")
	}
	found := false
	for _, e := range m.Errors {
		if e.Kind == KindFatal && e.OffsetSet && e.Code == CodeTimestampRepair {
			found = true
		}
	}
	if !found {
		t.Fatalf("want offset-anchored fatal timestamp error, got %+v", m.Errors)
	}
}

// TestIntegrationIndexReuseAndRebuild covers the digest lifecycle: a second
// run reuses the index, a valid content change forces a rebuild, and the
// metrics from two identical runs are byte-stable.
func TestIntegrationIndexReuseAndRebuild(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if _, _, err := GenerateFixtures(dir, DefaultFixtureParams()); err != nil {
		t.Fatal(err)
	}
	specs, err := LoadSpecs(dir)
	if err != nil {
		t.Fatal(err)
	}
	opts := PipelineOptions{InputDir: dir, WorkDir: work, ChunkSize: 12, TargetBPS: 4_000_000}
	first, err := RunBatch(specs, opts)
	if err != nil || !first.Success {
		t.Fatalf("first run failed: %v", err)
	}
	if first.Metrics.IndexRebuilt != len(specs) {
		t.Fatalf("first run must rebuild all indexes, got %d/%d", first.Metrics.IndexRebuilt, len(specs))
	}
	second, err := RunBatch(specs, opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.Metrics.IndexRebuilt != 0 {
		t.Fatalf("unchanged input must reuse indexes, rebuilt=%d", second.Metrics.IndexRebuilt)
	}
	// All statistics except the rebuild counter must be identical.
	a, b := first.Metrics, second.Metrics
	a.IndexRebuilt, b.IndexRebuilt = 0, 0
	if b != a {
		t.Fatalf("repeated run metrics differ:\n%+v\n%+v", a, b)
	}

	// Append one contiguous, valid frame to the H.264 capture.
	capture := filepath.Join(dir, DefaultFixtureNames().H264)
	appended := appendContiguousFrame(t, capture)
	defer func() { _ = os.WriteFile(capture, appended.original, 0o644) }()
	third, err := RunBatch(specs, opts)
	if err != nil {
		t.Fatal(err)
	}
	var rebuilt bool
	for _, tr := range third.Tracks {
		if tr.Track == "video_h264" {
			rebuilt = tr.IndexRebuilt
			if tr.FrameCount != 61 {
				t.Fatalf("mutated track should index 61 frames, got %d", tr.FrameCount)
			}
		}
	}
	if !rebuilt {
		t.Fatal("changed input must invalidate and rebuild the index")
	}
}

type mutation struct{ original []byte }

// appendContiguousFrame adds a valid marker frame with the next contiguous
// timestamp so the change is decodable but changes the content digest.
func appendContiguousFrame(t *testing.T, path string) mutation {
	t.Helper()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pkt := rtp.Packet{
		Header:  rtp.Header{SequenceNumber: 60, Timestamp: 60 * 3000, Marker: true, Version: 2, PayloadType: 96, SSRC: 1},
		Payload: []byte{0x41, 0x99},
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(raw)))
	out := append(append(append([]byte(nil), original...), l[:]...), raw...)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return mutation{original: original}
}

// TestIntegrationTruncatedNotIndexed asserts the truncated tail unit is
// absent from the index and reported with a byte offset.
func TestIntegrationTruncatedNotIndexed(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := GenerateFixtures(dir, DefaultFixtureParams()); err != nil {
		t.Fatal(err)
	}
	specs, _ := LoadSpecs(dir)
	m, err := RunBatch(specs, PipelineOptions{
		InputDir: dir, WorkDir: filepath.Join(dir, "w"), ChunkSize: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	var sawTruncation bool
	for _, e := range m.Errors {
		if e.Code == CodeTruncatedUnit && e.OffsetSet {
			sawTruncation = true
		}
	}
	if !sawTruncation {
		t.Fatal("truncated unit not reported with an offset")
	}
	for _, tr := range m.Tracks {
		if tr.Track == "truncated_h264" && tr.FrameCount != 59 {
			t.Fatalf("truncated track must index 59 complete frames, got %d", tr.FrameCount)
		}
	}
}
