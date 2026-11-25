package pipeline

import (
	"path/filepath"
	"testing"

	"github.com/zld/media-regress/fixture"
	"github.com/zld/media-regress/internal/error"
	"github.com/zld/media-regress/manifest"
)

func fixtureData(t *testing.T, name string, format fixture.Format, corrupt int) (*fixture.Fixture, []byte) {
	t.Helper()
	f, err := fixture.Generate(fixture.Options{Name: name, Format: format, CorruptBlock: corrupt})
	if err != nil {
		t.Fatal(err)
	}
	return f, f.Bytes
}

func baseOptions(t *testing.T, data []byte) Options {
	t.Helper()
	return Options{
		Source: "valid.mp4", Data: data,
		SampleRate: 48000, Layout: "stereo", Loudness: -16,
		MaxBlocks: 4, MaxBytes: 32000,
	}
}

func TestRunValidAllStages(t *testing.T) {
	_, data := fixtureData(t, fixture.NameValid, fixture.FormatMP4, 0)
	res, err := Run(baseOptions(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.Stages != 5 {
		t.Fatalf("stages %d", res.Summary.Stages)
	}
	if res.Summary.QueuePeakBytes > 32000 {
		t.Fatalf("peak %d exceeds limit", res.Summary.QueuePeakBytes)
	}
	if len(res.Manifest.Track.Segments) != fixture.AudioBlocks {
		t.Fatalf("segments %d", len(res.Manifest.Track.Segments))
	}
	if res.Manifest.Digest == "" {
		t.Fatal("no manifest digest")
	}
	if res.Summary.Conflicts != 2 {
		t.Fatalf("conflicts %d want 2", res.Summary.Conflicts)
	}
	if errs := manifest.Verify(res.Manifest, res.Manifest.Track.Audio, true); len(errs) != 0 {
		t.Fatalf("verify: %v", errs)
	}
}

func TestRunDeterministicDigest(t *testing.T) {
	_, data := fixtureData(t, fixture.NameValid, fixture.FormatMP4, 0)
	a, err := Run(baseOptions(t, data))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(baseOptions(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if a.Manifest.Digest != b.Manifest.Digest {
		t.Fatalf("digests differ:\n%s\n%s", a.Manifest.Digest, b.Manifest.Digest)
	}
}

func TestRunCorruptBlockFailsStream(t *testing.T) {
	_, data := fixtureData(t, fixture.NameCorrupt, fixture.FormatMP4, 3)
	_, err := Run(baseOptions(t, data))
	if err == nil {
		t.Fatal("corrupt block must fail")
	}
	e, ok := merr.As(err)
	if !ok || e.Stage != merr.StageStream || e.TrackID != fixture.AudioTrackID || e.InputOffset == 0 {
		t.Fatalf("structured error: %v", err)
	}
}

func TestRunBoundaryReportsTimestamp(t *testing.T) {
	_, data := fixtureData(t, fixture.NameBoundary, fixture.FormatMP4, 0)
	_, err := Run(baseOptions(t, data))
	if err == nil {
		t.Fatal("truncated timestamps must fail")
	}
	if e, ok := merr.As(err); !ok || e.Kind != merr.KindTimestamp {
		t.Fatalf("expected timestamp error, got %v", err)
	}
}

func TestRunInterruptAndResumeMatches(t *testing.T) {
	dir := t.TempDir()
	_, data := fixtureData(t, fixture.NameValid, fixture.FormatMP4, 0)
	opts := baseOptions(t, data)
	opts.CheckpointPath = filepath.Join(dir, "cp.json")
	opts.InterruptAfterBlock = 3
	if _, err := Run(opts); err == nil {
		t.Fatal("interrupt must abort the first run")
	}
	opts.Resume = true
	opts.InterruptAfterBlock = 0
	res, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.ResumeBlock != 3 {
		t.Fatalf("resume_block %d want 3", res.Summary.ResumeBlock)
	}
	if res.Summary.RecomputedBlocks >= fixture.AudioBlocks {
		t.Fatalf("recomputed %d not fewer than total %d", res.Summary.RecomputedBlocks, fixture.AudioBlocks)
	}
	plain, err := Run(baseOptions(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Digest != plain.Manifest.Digest {
		t.Fatalf("resumed digest %s != plain %s", res.Manifest.Digest, plain.Manifest.Digest)
	}
	if len(res.Manifest.Track.Segments) != fixture.AudioBlocks {
		t.Fatalf("resumed manifest has %d segments", len(res.Manifest.Track.Segments))
	}
}

func TestRunCacheHitAndTamper(t *testing.T) {
	dir := t.TempDir()
	_, data := fixtureData(t, fixture.NameValid, fixture.FormatMP4, 0)
	opts := baseOptions(t, data)
	opts.CacheDir = dir
	first, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheStats == nil || second.CacheStats.Hits != fixture.AudioBlocks {
		t.Fatalf("expected all hits, got %+v", second.CacheStats)
	}
	if first.Manifest.Digest != second.Manifest.Digest {
		t.Fatal("cached run digest differs")
	}
	opts.TamperCache = true
	tampered, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if tampered.Summary.CacheRejected != 1 {
		t.Fatalf("cache_rejected %d want 1", tampered.Summary.CacheRejected)
	}
	if tampered.Manifest.Digest != first.Manifest.Digest {
		t.Fatal("tampered cache changed the manifest digest")
	}
}

func TestRunSlowConsumerKeepsPeak(t *testing.T) {
	_, data := fixtureData(t, fixture.NameValid, fixture.FormatMP4, 0)
	opts := baseOptions(t, data)
	opts.SlowConsumerMs = 1
	res, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.QueuePeakBytes > opts.MaxBytes {
		t.Fatalf("peak %d exceeds %d", res.Summary.QueuePeakBytes, opts.MaxBytes)
	}
}

func TestRunMXF(t *testing.T) {
	_, data := fixtureData(t, fixture.NameValid, fixture.FormatMXF, 0)
	opts := baseOptions(t, data)
	opts.Source = "valid.mxf"
	res, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Format != manifest.FormatMXF {
		t.Fatalf("format %s", res.Manifest.Format)
	}
}

func TestRunDropSegmentFailsVerification(t *testing.T) {
	_, data := fixtureData(t, fixture.NameValid, fixture.FormatMP4, 0)
	opts := baseOptions(t, data)
	opts.DropSegment = 3
	res, err := Run(opts)
	if err != nil {
		t.Fatal(err)
	}
	if errs := manifest.Verify(res.Manifest, res.Manifest.Track.Audio, true); len(errs) == 0 {
		t.Fatal("dropped segment must fail verification")
	}
}
