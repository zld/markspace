package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"mediarun/internal/fault"
	"mediarun/internal/gen"
	"mediarun/internal/provenance"
)

// runFixture generates a fixture and executes the pipeline over it.
func runFixture(t *testing.T, opts gen.Options, cfg Config, resume bool) (*provenance.Manifest, *Runner, string) {
	t.Helper()
	dir := t.TempDir()
	opts.Dir = dir
	if _, err := gen.Generate(opts); err != nil {
		t.Fatalf("generate: %v", err)
	}
	r, err := New(dir, Inputs{
		FramesPath: filepath.Join(dir, "frames.mfr"),
		AudioPath:  filepath.Join(dir, "audio.rtp"),
	}, cfg)
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Resume(resume); err != nil {
		t.Fatalf("resume: %v", err)
	}
	m, err := r.Run()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return m, r, dir
}

func TestHealthyRunProducesVerifiableSegments(t *testing.T) {
	m, _, dir := runFixture(t, gen.Options{SegmentCount: 4, FramesPerSeg: 6}, DefaultConfig(), false)
	if m.Metrics.Segments != 4 {
		t.Fatalf("segments = %d", m.Metrics.Segments)
	}
	if m.Metrics.ParseErrors != 0 || m.Metrics.IncompleteSegments != 0 {
		t.Fatalf("unexpected damage: %+v", m.Metrics)
	}
	vr, err := Verify(dir, m)
	if err != nil {
		t.Fatal(err)
	}
	if vr.RoundtripRatio() != 100 {
		t.Fatalf("roundtrip = %d misses=%v", vr.RoundtripRatio(), vr.Misses)
	}
	if vr.KeyframeRatio() != 100 {
		t.Fatalf("keyframe = %d misses=%v", vr.KeyframeRatio(), vr.Misses)
	}
	if !vr.SegmentsEqual || !vr.Idempotent {
		t.Fatalf("verify = %+v", vr)
	}
	for _, seg := range m.Segments {
		if seg.Bytes == 0 {
			t.Fatalf("empty segment %d", seg.Index)
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(seg.Path))); err != nil {
			t.Fatalf("segment file missing: %v", err)
		}
	}
}

func TestResumeIsByteIdentical(t *testing.T) {
	m1, _, dir := runFixture(t, gen.Options{SegmentCount: 3, FramesPerSeg: 5}, DefaultConfig(), false)
	first, _ := provenance.CanonicalJSON(m1)

	r, err := New(dir, Inputs{
		FramesPath: filepath.Join(dir, "frames.mfr"),
		AudioPath:  filepath.Join(dir, "audio.rtp"),
	}, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Resume(true); err != nil {
		t.Fatal(err)
	}
	if r.ResumedStage() != "complete" {
		t.Fatalf("resume stage = %s", r.ResumedStage())
	}
	m2, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := provenance.CanonicalJSON(m2)
	if string(first) != string(second) {
		t.Fatalf("resumed manifest changed\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !provenance.EqualHashes(m1.SegmentHashes(), m2.SegmentHashes()) {
		t.Fatal("cached segment digests changed")
	}
	vr, err := Verify(dir, m2)
	if err != nil {
		t.Fatal(err)
	}
	ApplyBaseline(vr, r.PreviousManifest())
	if !vr.Idempotent {
		t.Fatalf("not idempotent: %+v", vr)
	}
}

func TestConfigChangeInvalidatesAndRewrites(t *testing.T) {
	_, _, dir := runFixture(t, gen.Options{SegmentCount: 2, FramesPerSeg: 5}, DefaultConfig(), false)
	cfg := DefaultConfig()
	cfg.KeyframeEvery = 4 // changes frame flags, hence frame digests
	r, err := New(dir, Inputs{
		FramesPath: filepath.Join(dir, "frames.mfr"),
		AudioPath:  filepath.Join(dir, "audio.rtp"),
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Resume(true); err != nil {
		t.Fatal(err)
	}
	if r.ResumedStage() != "parse" {
		t.Fatalf("changed config resumed at %s", r.ResumedStage())
	}
	if _, err := r.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestParseFaultsPropagateToManifest(t *testing.T) {
	m, _, dir := runFixture(t, gen.Options{Inject: []string{"truncated", "checksum"}}, DefaultConfig(), false)
	if m.Metrics.ParseErrors < 2 {
		t.Fatalf("parse errors = %d", m.Metrics.ParseErrors)
	}
	off := int64(-1)
	for _, f := range m.Faults {
		if f.Kind == "checksum_mismatch" {
			if off < 0 || f.Offset < off {
				off = f.Offset
			}
		}
	}
	if off < 0 {
		t.Fatalf("no checksum fault: %+v", m.Faults)
	}
	if m.Metrics.RecoveredVideo == 0 {
		t.Fatal("no resynchronisation recorded")
	}
	// Output must still be complete and verifiable despite the damage.
	vr, err := Verify(dir, m)
	if err != nil {
		t.Fatal(err)
	}
	if vr.RoundtripRatio() != 100 || vr.KeyframeRatio() != 100 {
		t.Fatalf("verify = %+v", vr)
	}
}

func TestAudioGapRecoveryReachesManifest(t *testing.T) {
	m, _, _ := runFixture(t, gen.Options{Inject: []string{"gap"}}, DefaultConfig(), false)
	if m.Metrics.RecoveredAudio == 0 {
		t.Fatal("small gap was not concealed")
	}
	if m.Metrics.IncompleteSegments == 0 {
		t.Fatal("large gap did not mark a fragment incomplete")
	}
	var loss, incomplete bool
	for _, f := range m.Faults {
		if f.Kind == string(fault.KindLoss) {
			loss = true
		}
		if f.Kind == string(fault.KindIncomplete) {
			incomplete = true
		}
	}
	if !loss || !incomplete {
		t.Fatalf("missing recovery faults: %+v", m.Faults)
	}
}

func TestDuplicateAndOverflowAreRecorded(t *testing.T) {
	m, _, _ := runFixture(t, gen.Options{Inject: []string{"dupe", "overflow"}}, DefaultConfig(), false)
	var dupe, overflow bool
	for _, f := range m.Faults {
		switch f.Kind {
		case "duplicate_packet":
			dupe = true
		case "window_overflow":
			overflow = true
			if f.Offset < 0 {
				t.Fatalf("overflow without source offset: %+v", f)
			}
		}
	}
	if !dupe || !overflow {
		t.Fatalf("faults = %+v", m.Faults)
	}
}

func TestDesyncPropagatesFromSyncToManifest(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ToleranceTicks = 10
	cfg.SyncOffsetTicks = 9000 // 100 ms at 90 kHz
	m, _, _ := runFixture(t, gen.Options{}, cfg, false)
	if m.Metrics.UnsyncedSegments == 0 {
		t.Fatal("desync not surfaced")
	}
	found := false
	for _, f := range m.Faults {
		if f.Kind == "out_of_sync" {
			found = true
		}
	}
	if !found {
		for _, seg := range m.Segments {
			for _, f := range seg.Faults {
				if f.Kind == "out_of_sync" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("no out_of_sync fault: %+v", m.Faults)
	}
}

func TestRunnerRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Timescale = 0
	if _, err := New(dir, Inputs{FramesPath: "f", AudioPath: "a"}, cfg); err == nil {
		t.Fatal("zero timescale accepted")
	}
	cfg = DefaultConfig()
	cfg.Width = 99999
	if _, err := New(dir, Inputs{FramesPath: "f", AudioPath: "a"}, cfg); err == nil {
		t.Fatal("oversize width accepted")
	}
}
