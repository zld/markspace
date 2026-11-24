package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediaaudit/internal/checkpoint"
	"mediaaudit/internal/fixtures"
	"mediaaudit/internal/model"
	"mediaaudit/internal/mp4"
	"mediaaudit/internal/render"
	"mediaaudit/internal/timeline"
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

func TestInterruptBeyondLastChunkCompletes(t *testing.T) {
	// Asking to stop after every chunk must not turn a finished batch into a
	// reported interruption.
	m, _ := setup(t, fixtures.ScenarioNormal)
	in := inputs(t, m, t.TempDir())
	in.InterruptAfter = 3 // exactly the chunk count
	st, err := Run(in)
	if err != nil {
		t.Fatalf("finished batch reported as interrupted: %v", err)
	}
	if st.CommittedChunks != 3 || st.Metrics.SegmentHashMatch.Value() < 1.0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestResumeNeverDecreasesCommittedChunks(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioNormal)
	out := t.TempDir()
	in := inputs(t, m, out)
	seen := 0
	for i := 1; i <= 3; i++ {
		in.InterruptAfter = i
		st, err := Run(in)
		var interrupted *InterruptedError
		if err != nil && !errors.As(err, &interrupted) {
			t.Fatal(err)
		}
		if st.CommittedChunks < seen {
			t.Fatalf("committed dropped from %d to %d", seen, st.CommittedChunks)
		}
		seen = st.CommittedChunks
	}
	in.InterruptAfter = 0
	st, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if st.CommittedChunks != 3 || st.ReplayedChunks != 0 {
		t.Fatalf("final committed=%d replayed=%d", st.CommittedChunks, st.ReplayedChunks)
	}
}

func TestConfigDigestIgnoresSubtitleOrder(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioNormal)
	a := inputs(t, m, t.TempDir())
	b := inputs(t, m, t.TempDir())
	b.OutDir = a.OutDir
	if len(b.Subtitles) < 2 {
		t.Skip("fixture has a single subtitle track")
	}
	b.Subtitles[0], b.Subtitles[1] = b.Subtitles[1], b.Subtitles[0]
	if configDigest(a) != configDigest(b) {
		t.Fatal("subtitle slice order must not change the configuration digest")
	}
}

func TestBrokenFrameFailureCountSurvivesResume(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioBroken)
	out := t.TempDir()
	in := inputs(t, m, out)
	// Short reads live at the very end of the clip, so use small chunks and
	// stop before the last one, letting the resume render it.
	in.ChunkSize = 8
	in.InterruptAfter = 1
	if _, err := Run(in); err == nil {
		t.Fatal("expected interruption")
	}
	in.InterruptAfter = 0
	st, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if st.FrameFailures == 0 {
		t.Fatal("short-read frames must still be counted after a resume")
	}
	// A fresh run must report the same count.
	fresh := inputs(t, m, t.TempDir())
	fresh.Resume = false
	st2, err := Run(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if st.FrameFailures != st2.FrameFailures {
		t.Fatalf("resume counted %d, fresh counted %d", st.FrameFailures, st2.FrameFailures)
	}
}

func TestKeyframeHitRateCountsSamplingPoints(t *testing.T) {
	index := string(mp4.OriginIndex)
	scan := string(mp4.OriginScan)
	frames := []render.FrameRecord{
		{Origin: index}, {Origin: index}, {Origin: scan},
		{Origin: index, Error: model.NewError(model.CodeSampleRead, "render", "v1", model.InputPos{}, "x")},
	}
	got := keyframeHitRate(frames)
	// Failed points are excluded: 2 of 3 successful points hit the index.
	if got.Num != 2 || got.Den != 3 {
		t.Fatalf("ratio %d/%d", got.Num, got.Den)
	}
	if keyframeHitRate(nil).Den != 0 {
		t.Fatal("no frames means no denominator")
	}
}

func TestSamplesUsePresentationOrder(t *testing.T) {
	// A B-frame layout: decode order 0,1,2 but presentation order 0,2,1 via
	// ctts offsets. The timeline and sampling lookup must see presentation
	// order, otherwise cut points go backwards.
	interval, _ := model.ParseRat("1/25")
	spec := fixtures.MP4Spec{
		TrackID: 1, Timescale: 1000, Width: 8, Height: 8,
		Sizes: []uint32{10, 10, 10}, Deltas: []uint32{100, 100, 100},
		Ctts:      []int64{0, 200, 100},
		Keyframes: []int{1, 3},
		Label:     "b",
	}
	data, err := fixtures.MP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mp4Path := dir + "/b.mp4"
	if err := os.WriteFile(mp4Path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	in := Inputs{
		OutDir: dir + "/cp", MP4Path: mp4Path, Width: 8, Height: 8,
		Interval: interval, ChunkSize: 16, MaxDriftPPM: 500, FallbackScan: true,
	}
	st, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if st.Items != 3 || st.Frames == 0 {
		t.Fatalf("items=%d frames=%d", st.Items, st.Frames)
	}
	p, err := checkpoint.Open(in.OutDir)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := p.ReadPayload(0)
	if err != nil {
		t.Fatal(err)
	}
	prev := model.RatFromInt(-1)
	for _, it := range payload.Items {
		if it.Kind == timeline.ItemVideo {
			if it.Span.Start.Cmp(prev) < 0 {
				t.Fatalf("video items out of presentation order: %s after %s", it.Span.Start, prev)
			}
			prev = it.Span.Start
		}
	}
}

func TestSubtitleOnlyTimelineStillRenders(t *testing.T) {
	// The kernel accepts a run with no video; the timeline is subtitles-only
	// and every sample lookup honestly fails rather than faking frames.
	dir := t.TempDir()
	srt := filepath.Join(dir, "c.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:01,000\nhi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	interval, _ := model.ParseRat("1/2")
	in := Inputs{
		OutDir: filepath.Join(dir, "cp"), Subtitles: []SubtitleInput{{TrackID: "s1", Path: srt}},
		Width: 8, Height: 8, Interval: interval, ChunkSize: 16, FallbackScan: true,
	}
	st, err := Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if st.Items == 0 {
		t.Fatal("subtitle cues must still form a timeline")
	}
	if st.Metrics.KeyframeIndexHitRate.Den != 0 {
		t.Fatalf("no video means no keyframe ratio, got %d/%d",
			st.Metrics.KeyframeIndexHitRate.Num, st.Metrics.KeyframeIndexHitRate.Den)
	}
}

func TestVerifyWithoutResumeDoesNotWipeCheckpoint(t *testing.T) {
	m, _ := setup(t, fixtures.ScenarioNormal)
	out := t.TempDir()
	in := inputs(t, m, out)
	in.Resume = false
	if _, err := Run(in); err != nil {
		t.Fatal(err)
	}
	// A verify-only run without -resume must still see the committed chunks.
	in.VerifyOnly = true
	in.Resume = false
	st, err := Run(in)
	if err != nil {
		t.Fatalf("verify wiped or ignored the checkpoint: %v", err)
	}
	if st.CommittedChunks != 3 || st.VerifiedChunks != 3 {
		t.Fatalf("committed=%d verified=%d", st.CommittedChunks, st.VerifiedChunks)
	}
}

func TestUnreadableVideoIsRecoverable(t *testing.T) {
	// A missing video must fail only that input, leaving the batch to finish
	// with exit code 2 instead of aborting as a process-level I/O error.
	dir := t.TempDir()
	interval, _ := model.ParseRat("1/2")
	in := Inputs{
		OutDir: filepath.Join(dir, "cp"), MP4Path: filepath.Join(dir, "missing.mp4"),
		Width: 8, Height: 8, Interval: interval, ChunkSize: 4, FallbackScan: true,
	}
	st, err := Run(in)
	if err != nil {
		t.Fatalf("missing video must not be fatal: %v", err)
	}
	if st.ExitCode() != 2 {
		t.Fatalf("exit=%d want 2", st.ExitCode())
	}
}

func TestRTPAuditHandlesReorderedFirstPacket(t *testing.T) {
	// A stream whose first packet is slightly out of order must not be read
	// as a huge unsigned difference (which would look like massive drift).
	dir := t.TempDir()
	data, err := fixtures.RTPStream(fixtures.RTPSpec{
		Timescale: 90000, RefTimescale: 90000, Count: 50,
		StartTS: 100000, StepTS: 9000, StepRef: 9000,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a.rtp")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	res := AuditRTPStreams(map[string]string{"a": path}, 500)
	if len(res.Tracks) != 1 || res.Tracks[0].Failed {
		t.Fatalf("clean stream failed: %+v", res.Tracks)
	}
	if res.Tracks[0].Wraps != 0 {
		t.Fatalf("wraps=%d", res.Tracks[0].Wraps)
	}
}
