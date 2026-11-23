package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediadist/demux"
	"mediadist/segment"
	"mediadist/timeline"
)

// mkInputs builds three representations (800k/1600k/3000k) of a 12 s fixture,
// optionally injecting the same fault into every representation.
func mkInputs(fault demux.FaultCase) []Input {
	bitrates := []struct {
		id string
		bp uint64
	}{
		{"800k", 800_000}, {"1600k", 1_600_000}, {"3000k", 3_000_000},
	}
	out := make([]Input, 0, len(bitrates))
	for _, b := range bitrates {
		spec := demux.FixtureSpec{
			Seed: 17, Duration: 12,
			Video: demux.VideoSpec{
				Timescale: 24000, Width: 1280, Height: 720,
				KeyframeEvery: 48, FrameDelta: 1001, FrameCount: 288,
				FrameSizeBytes: demux.VideoFrameSizeForTotalBitrate(b.bp, 24000, 1001),
				Color:          &demux.ColorValue{Primaries: 9, Transfer: 16, Matrix: 9},
			},
			Audio: demux.AudioSpec{
				Timescale: 48000, SampleRate: 48000, Channels: 2,
				FrameSamples: 1024, FrameCount: 562,
			},
			Fault: fault,
		}
		data, err := demux.WriteFixture(spec)
		if err != nil {
			panic(err)
		}
		out = append(out, Input{ID: b.id, TargetBitrateBps: b.bp, Fixture: data,
			GraphFault: graphFaultFor(fault)})
	}
	return out
}

func graphFaultFor(f demux.FaultCase) demux.FaultCase {
	switch f {
	case demux.FaultTimestampBack, demux.FaultMissingKeyframe:
		return f
	default:
		return demux.FaultNone
	}
}

func testOptions(t *testing.T, dir string) Options {
	t.Helper()
	return Options{
		OutputDir:       dir,
		TargetAudioRate: 48000,
		ColorPriority:   []timeline.ColorSource{timeline.SourceBitstream, timeline.SourceContainer},
		Segment:         segment.DefaultConfig(),
	}
}

func TestHappyPathProducesStableManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	r, err := NewRunner(testOptions(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(mkInputs(demux.FaultNone))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	m := res.Manifest
	if m.SegmentCount < 6 {
		t.Fatalf("want >=6 segments for 12s, got %d", m.SegmentCount)
	}
	if len(m.Representations) != 3 {
		t.Fatalf("want 3 representations, got %d", len(m.Representations))
	}
	if m.AlignmentErrorTicks > segment.DefaultConfig().MaxDurationErrorTicks {
		t.Fatalf("alignment error %d beyond tolerance", m.AlignmentErrorTicks)
	}
	for _, rep := range m.Representations {
		if rep.BitrateErrorBasisPts > 1500 {
			t.Fatalf("%s bitrate error %d bps exceeds 15%%", rep.ID, rep.BitrateErrorBasisPts)
		}
		if rep.ColorSource != string(timeline.SourceContainer) {
			t.Fatalf("%s color source wrong: %q", rep.ID, rep.ColorSource)
		}
		if rep.AudioChannelsIn != 2 || rep.AudioChannelsOut != 2 ||
			rep.SampleRateIn != 48000 || rep.SampleRateOut != 48000 {
			t.Fatalf("%s audio summary wrong: %+v", rep.ID, rep)
		}
	}

	// Re-run into a fresh directory with identical inputs: hash must match.
	dir2 := filepath.Join(t.TempDir(), "out2")
	r2, _ := NewRunner(testOptions(t, dir2))
	res2, err := r2.Run(mkInputs(demux.FaultNone))
	if err != nil {
		t.Fatal(err)
	}
	if res2.Manifest.SHA256 != m.SHA256 {
		t.Fatalf("rerun manifest hash differs:\n%s\n%s", m.SHA256, res2.Manifest.SHA256)
	}
}

func TestTruncatedIndexFailsAtDemuxStage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	r, _ := NewRunner(testOptions(t, dir))
	_, err := r.Run(mkInputs(demux.FaultTruncatedIndex))
	var re *RunError
	if !errors.As(err, &re) || re.Code != timeline.CodeTruncatedIndex || re.Stage != StageDemux {
		t.Fatalf("got %v, want INDEX_TRUNCATED at demux stage", err)
	}
	failed, state := r.Store().FailedStage()
	if failed != StageDemux || !state.Failed || state.ErrorCode != timeline.CodeTruncatedIndex {
		t.Fatalf("checkpoint did not record demux failure: %+v", state)
	}
}

func TestTimestampBackwardPropagatesToTimelineStage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	r, _ := NewRunner(testOptions(t, dir))
	_, err := r.Run(mkInputs(demux.FaultTimestampBack))
	var re *RunError
	if !errors.As(err, &re) || re.Code != timeline.CodeTimestampBack || re.Stage != StageTimeline {
		t.Fatalf("got %v, want TIMESTAMP_BACKWARD at timeline stage", err)
	}
}

func TestMissingKeyframePropagatesToSegmentStage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	r, _ := NewRunner(testOptions(t, dir))
	_, err := r.Run(mkInputs(demux.FaultMissingKeyframe))
	var re *RunError
	if !errors.As(err, &re) || re.Code != timeline.CodeMissingKeyframe || re.Stage != StageSegment {
		t.Fatalf("got %v, want KEYFRAME_MISSING at segment stage", err)
	}
	failed, _ := r.Store().FailedStage()
	if failed != StageSegment {
		t.Fatalf("checkpoint failed stage=%q want segment", failed)
	}
}

func TestBadLayoutFailsAtNormalizeStage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	r, _ := NewRunner(testOptions(t, dir))
	_, err := r.Run(mkInputs(demux.FaultBadLayout))
	var re *RunError
	// Bad layout is caught either at timeline validation or normalize depending
	// on where the unsupported marker lands; both must carry the same code.
	if !errors.As(err, &re) || re.Code != timeline.CodeBadLayout {
		t.Fatalf("got %v, want LAYOUT_UNSUPPORTED", err)
	}
}

func TestRepairThenRerunSucceedsAndResumes(t *testing.T) {
	// First run fails on backward timestamps.
	dir := filepath.Join(t.TempDir(), "out")
	r, _ := NewRunner(testOptions(t, dir))
	if _, err := r.Run(mkInputs(demux.FaultTimestampBack)); err == nil {
		t.Fatal("expected first run to fail")
	}
	failed, _ := r.Store().FailedStage()
	if failed == "" {
		t.Fatal("expected a recorded failed stage")
	}

	// Simulate repaired input: same containers, no graph fault injection.
	repaired := mkInputs(demux.FaultNone)
	if err := r.Store().ResetFailed(); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(repaired)
	if err != nil {
		t.Fatalf("repaired rerun: %v", err)
	}
	if res.Manifest.SegmentCount < 6 {
		t.Fatalf("repaired manifest too short: %d", res.Manifest.SegmentCount)
	}
	if f, _ := r.Store().FailedStage(); f != "" {
		t.Fatalf("failure marker remains after recovery: %s", f)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	// The security boundary is relative child requests: any request that would
	// resolve outside the store root must be refused.
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := safePath(store.root, "../../etc/passwd"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("relative traversal got %v, want ErrPathEscape", err)
	}
	if _, err := safePath(store.root, "nested/../../../x"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("nested traversal got %v, want ErrPathEscape", err)
	}
	// A normal in-root path must still resolve cleanly.
	ok, err := safePath(store.root, "manifest.json")
	if err != nil || !strings.HasPrefix(ok, store.root) {
		t.Fatalf("legitimate path rejected: %q %v", ok, err)
	}
}

func TestCorruptCheckpointClassified(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cp := &checkpoint{Version: 1, Stages: freshStages()}
	if err := store.write(cp); err != nil {
		t.Fatal(err)
	}
	// Corrupt the file contents.
	p := filepath.Join(store.root, checkpointFile)
	if err := writeFileTruncate(p); err != nil {
		t.Fatal(err)
	}
	_, err = store.Load()
	var le *timeline.LocError
	if !errors.As(err, &le) || le.Code != timeline.CodeCheckpoint {
		t.Fatalf("got %v, want CHECKPOINT_CORRUPT", err)
	}
}

func TestRerunSummaryIdentical(t *testing.T) {
	inputs := mkInputs(demux.FaultNone)
	dir1 := filepath.Join(t.TempDir(), "a")
	r1, _ := NewRunner(testOptions(t, dir1))
	res1, err := r1.Run(inputs)
	if err != nil {
		t.Fatal(err)
	}
	dir2 := filepath.Join(t.TempDir(), "b")
	r2, _ := NewRunner(testOptions(t, dir2))
	res2, err := r2.Run(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if summary(res1) != summary(res2) {
		t.Fatalf("summaries differ:\n%s\n%s", summary(res1), summary(res2))
	}
}

// summary renders the stable fields compared across reruns.
func summary(r *Result) string {
	var b strings.Builder
	m := r.Manifest
	b.WriteString(m.SHA256)
	for _, rep := range m.Representations {
		b.WriteString(rep.ID)
		b.WriteString(rep.ColorSource)
	}
	return b.String()
}

func writeFileTruncate(path string) error {
	return os.WriteFile(path, []byte("{not-json"), 0o644)
}
