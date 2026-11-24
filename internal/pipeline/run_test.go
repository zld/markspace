package pipeline

import (
	"errors"
	"path/filepath"
	"testing"

	"mediaaudit/internal/fixtures"
	"mediaaudit/internal/model"
)

func setup(t *testing.T, scenario string) (fixtures.Manifest, string) {
	t.Helper()
	root := t.TempDir()
	if _, err := fixtures.Generate(root); err != nil {
		t.Fatal(err)
	}
	m, err := fixtures.LoadManifest(filepath.Join(root, scenario))
	if err != nil {
		t.Fatal(err)
	}
	return m, root
}

func inputs(t *testing.T, m fixtures.Manifest, out string) Inputs {
	t.Helper()
	interval, _ := model.ParseRat("1/25")
	in := Inputs{
		OutDir: out, MP4Path: m.MP4, TimecodePath: m.Timecodes,
		TimecodeMode: m.TimecodeMode, Width: 16, Height: 16, Interval: interval,
		ChunkSize: 32, MaxDriftPPM: 500, FallbackScan: true, Resume: true,
		RTPStreams: m.RTP,
	}
	for id, p := range m.Subtitles {
		in.Subtitles = append(in.Subtitles, SubtitleInput{TrackID: id, Path: p})
	}
	return in
}

func TestNormalScenarioPerfectMetrics(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioNormal)
	st, err := Run(inputs(t, m, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Metrics.AllHeld() {
		t.Fatalf("metrics %+v", st.Metrics)
	}
	if st.ExitCode() != 0 || st.FrameFailures != 0 {
		t.Fatalf("exit=%d failures=%d", st.ExitCode(), st.FrameFailures)
	}
	if st.Chunks != 3 || st.CommittedChunks != 3 {
		t.Fatalf("chunks=%d committed=%d", st.Chunks, st.CommittedChunks)
	}
}

func TestResumeSkipsCommittedChunks(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioNormal)
	out := t.TempDir()
	in := inputs(t, m, out)
	in.InterruptAfter = 2
	st, err := Run(in)
	var interrupted *InterruptedError
	if !errors.As(err, &interrupted) {
		t.Fatalf("want interruption, got %v", err)
	}
	if st.CommittedChunks != 2 {
		t.Fatalf("committed %d", st.CommittedChunks)
	}

	in.InterruptAfter = 0
	st2, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if st2.CommittedChunks != 3 || st2.ReplayedChunks != 0 || st2.VerifiedChunks != 2 {
		t.Fatalf("resume stats committed=%d replayed=%d verified=%d",
			st2.CommittedChunks, st2.ReplayedChunks, st2.VerifiedChunks)
	}
	if st2.Metrics.SegmentHashMatch.Value() < 1.0 {
		t.Fatalf("segment hash match %s", st2.Metrics.SegmentHashMatch)
	}
}

func TestNonResumeReproducesResumeHashes(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioNormal)
	outResume := t.TempDir()
	in := inputs(t, m, outResume)
	in.InterruptAfter = 2
	if _, err := Run(in); err == nil {
		t.Fatal("expected interruption")
	}
	in.InterruptAfter = 0
	first, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh run into a different directory must match chunk for chunk.
	in2 := inputs(t, m, t.TempDir())
	in2.Resume = false
	second, err := Run(in2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Manifest.Chunks) != len(second.Manifest.Chunks) {
		t.Fatal("chunk count differs")
	}
	for i := range first.Manifest.Chunks {
		if first.Manifest.Chunks[i].Digest != second.Manifest.Chunks[i].Digest {
			t.Fatalf("chunk %d digest differs", i)
		}
	}
}

func TestCheckpointMismatchRefusesResume(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioNormal)
	out := t.TempDir()
	in := inputs(t, m, out)
	in.InterruptAfter = 1
	if _, err := Run(in); err == nil {
		t.Fatal("expected interruption")
	}
	// Change a parameter that affects generated digests.
	in.InterruptAfter = 0
	in.Interval, _ = model.ParseRat("1/50")
	_, err := Run(in)
	if err == nil {
		t.Fatal("changed parameters must refuse to resume")
	}
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeCheckpointMismatch {
		t.Fatalf("want checkpoint_mismatch, got %v", err)
	}
}

func TestBrokenScenarioReportsFallbackAndShortReads(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioBroken)
	st, err := Run(inputs(t, m, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if st.ExitCode() != 2 {
		t.Fatalf("exit=%d want 2", st.ExitCode())
	}
	var fallback, shortRead bool
	for _, e := range st.Errors {
		if e.Code == model.CodeIndexFallback {
			fallback = true
			if e.Pos.Offset == 0 {
				t.Fatal("fallback must cite the damaged index offset")
			}
		}
		if e.Code == model.CodeSampleRead {
			shortRead = true
		}
	}
	if !fallback || !shortRead {
		t.Fatalf("fallback=%v shortRead=%v errors=%v", fallback, shortRead, st.Errors)
	}
	if st.Metrics.KeyframeIndexHitRate.Value() >= 1.0 {
		t.Fatal("a fully scanned index cannot score a perfect hit rate")
	}
}

func TestEdgeScenarioIsolatesDriftingTrack(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioEdge)
	st, err := Run(inputs(t, m, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if st.ExitCode() != 2 {
		t.Fatalf("exit=%d want 2", st.ExitCode())
	}
	byID := map[string]RTPTrackResult{}
	for _, tr := range st.RTP.Tracks {
		byID[tr.TrackID] = tr
	}
	if !byID["a1-wrap"].Failed && byID["a1-wrap"].Wraps != 1 {
		t.Fatalf("wrap track %+v", byID["a1-wrap"])
	}
	if !byID["a2-drift"].Failed || !byID["a2-drift"].Exceeded {
		t.Fatalf("drift track %+v", byID["a2-drift"])
	}
	if st.CommittedChunks == 0 {
		t.Fatal("media chunks must still commit despite the failing track")
	}
}

func TestVerifyOnlyRefusesMissingChunks(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioNormal)
	in := inputs(t, m, t.TempDir())
	in.VerifyOnly = true
	if _, err := Run(in); err == nil {
		t.Fatal("verify on an empty checkpoint must fail")
	}
}
