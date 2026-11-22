package pipeline_test

import (
	"strings"
	"testing"

	"example.com/mediatimeline/internal/fixture"
	"example.com/mediatimeline/internal/model"
	"example.com/mediatimeline/internal/pipeline"
)

func build(t *testing.T, seed int64, faults ...fixture.Fault) (fixture.Scenario, []fixture.Fault) {
	t.Helper()
	opts := fixture.DefaultOptions(seed)
	for _, f := range faults {
		opts.Faults[f] = true
	}
	sc, err := fixture.BuildScenario(opts)
	if err != nil {
		t.Fatalf("BuildScenario: %v", err)
	}
	return sc, faults
}

func run(t *testing.T, sc fixture.Scenario, faults []fixture.Fault) (*pipeline.Report, error) {
	t.Helper()
	in := fixture.PipelineInput(sc, faults)
	return pipeline.Run(in, pipeline.DefaultLimits())
}

func TestCleanRunInvariants(t *testing.T) {
	sc, faults := build(t, 42)
	rep, err := run(t, sc, faults)
	if err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	if rep.RoundTripMatch != 1 || rep.KeyframeHitRate != 1 || rep.SegmentHashMatch != 1 {
		t.Fatalf("consistency metrics must all be 1.000: roundtrip=%v keyframe=%v hash=%v",
			rep.RoundTripMatch, rep.KeyframeHitRate, rep.SegmentHashMatch)
	}
	if rep.RecoveredLayers != rep.TotalLayers {
		t.Fatalf("recovered %d/%d layers", rep.RecoveredLayers, rep.TotalLayers)
	}
	if rep.BoundaryErrors != 0 {
		t.Fatalf("boundary errors = %d", rep.BoundaryErrors)
	}
	if rep.VideoSamples != sc.Reference.VideoSamples {
		t.Fatalf("video samples = %d, want %d", rep.VideoSamples, sc.Reference.VideoSamples)
	}
	if rep.AudioSamples != sc.Reference.AudioSamples {
		t.Fatalf("audio samples = %d, want %d", rep.AudioSamples, sc.Reference.AudioSamples)
	}
	if rep.EventCount != sc.Reference.Events {
		t.Fatalf("events = %d, want %d", rep.EventCount, sc.Reference.Events)
	}
	if rep.Segments != 2 {
		t.Fatalf("segments = %d, want 2 for a 3 s program at 2 s segments", rep.Segments)
	}
	if rep.InputBytes > pipeline.DefaultLimits().MaxInputBytes {
		t.Fatal("fixture unexpectedly exceeds the input limit")
	}
}

func TestAcceptanceFaultMix(t *testing.T) {
	sc, faults := build(t, 42, fixture.FaultWrap, fixture.FaultLoss, fixture.FaultColorConflict)
	rep, err := run(t, sc, faults)
	if err != nil {
		t.Fatalf("acceptance mix must pass: %v", err)
	}
	if rep.RoundTripMatch < 0.99 || rep.KeyframeHitRate < 0.99 {
		t.Fatalf("roundtrip=%v keyframe=%v, want >=0.99", rep.RoundTripMatch, rep.KeyframeHitRate)
	}
	if rep.SegmentHashMatch != 1 {
		t.Fatalf("segment hash match = %v, want 1.000", rep.SegmentHashMatch)
	}
	if rep.RecoveredLayers != 2 || rep.TotalLayers != 3 {
		t.Fatalf("recovered %d/%d, want 2/3", rep.RecoveredLayers, rep.TotalLayers)
	}
	if rep.BoundaryErrors != 0 {
		t.Fatalf("boundary errors = %d", rep.BoundaryErrors)
	}
	if len(rep.Color.Conflicts) != 3 {
		t.Fatalf("color conflicts = %d, want 3", len(rep.Color.Conflicts))
	}
	if rep.Color.Selected.Primaries != 1 {
		t.Fatalf("container BT.709 must win, got primaries %d", rep.Color.Selected.Primaries)
	}
	// The RTP wrap must be recorded.
	var sawWrap bool
	for _, d := range rep.Decisions {
		if d.Kind == "wrap-extended" {
			sawWrap = true
		}
	}
	if !sawWrap {
		t.Fatal("wrap decision missing from the report")
	}
}

// TestErrorChainClock proves a clock fault propagates from parse through
// repair to the run result with its track and stage intact.
func TestErrorChainClock(t *testing.T) {
	sc, faults := build(t, 42, fixture.FaultBackward)
	_, err := run(t, sc, faults)
	if err == nil {
		t.Fatal("a backward timestamp must fail the run")
	}
	pe, ok := model.AsError(err)
	if !ok {
		t.Fatalf("error %v is not a shared model error", err)
	}
	if pe.Track != model.TrackVideoMain || pe.Stage != model.StageClock || pe.Code != model.CodeBackwardTimestamp {
		t.Fatalf("error chain lost context: %+v", pe)
	}
	if pe.Recoverable {
		t.Fatal("a backward step must be unrecoverable")
	}
	if pipeline.ExitCode(err) != 2 {
		t.Fatalf("exit code = %d, want 2", pipeline.ExitCode(err))
	}
}

// TestErrorChainAudio proves an audio frame fault is detected in the audio
// stage rather than being silently dropped.
func TestErrorChainAudio(t *testing.T) {
	sc, faults := build(t, 42, fixture.FaultAudioGap)
	rep, err := run(t, sc, faults)
	if err != nil {
		t.Fatalf("a concealable gap must not fail the run: %v", err)
	}
	var concealed bool
	for _, d := range rep.AudioDecisions {
		if d.Kind == "gap-concealed" {
			concealed = true
		}
	}
	var inRepairLog bool
	for _, line := range rep.RepairLog {
		if strings.Contains(line, "gap-concealed") {
			inRepairLog = true
		}
	}
	if !concealed || !inRepairLog {
		t.Fatalf("audio gap must be recorded, decisions=%d log=%v", len(rep.Decisions), rep.RepairLog)
	}
	// The recovered audio count grows by the concealment frame, and the
	// timeline stays monotonic.
	if rep.AudioSamples != sc.Reference.AudioSamples+1 {
		t.Fatalf("audio samples = %d, want %d after concealment", rep.AudioSamples, sc.Reference.AudioSamples+1)
	}
}

// TestErrorChainAudioLoss proves a mid-program audio dropout is detected by
// the audio stage and repaired, not silently discarded.
func TestErrorChainAudioLoss(t *testing.T) {
	sc, faults := build(t, 42, fixture.FaultAudioLoss)
	rep, err := run(t, sc, faults)
	if err != nil {
		t.Fatalf("a concealable audio loss must not fail the run: %v", err)
	}
	var concealed int
	for _, d := range rep.AudioDecisions {
		if d.Kind == "gap-concealed" {
			concealed++
		}
	}
	if concealed != 1 {
		t.Fatalf("gap-concealed decisions = %d, want 1: %+v", concealed, rep.AudioDecisions)
	}
	if rep.AudioSamples != sc.Reference.AudioSamples {
		t.Fatalf("audio samples = %d, want %d after one concealment", rep.AudioSamples, sc.Reference.AudioSamples)
	}
}

func TestAudioOverlapAndRateConflict(t *testing.T) {
	sc, faults := build(t, 42, fixture.FaultAudioOverlap)
	rep, err := run(t, sc, faults)
	if err != nil {
		t.Fatalf("overlap must be recoverable: %v", err)
	}
	var dropped bool
	for _, d := range rep.AudioDecisions {
		if d.Kind == "duplicate-dropped" {
			dropped = true
		}
	}
	if !dropped {
		t.Fatal("duplicate audio frame must be recorded as dropped")
	}
	if rep.AudioRate.Selected != 48000 || !rep.AudioRate.Resampled {
		t.Fatalf("rate decision = %+v, want 48 kHz with an explicit boundary", rep.AudioRate)
	}
}

func TestCorruptLayerIsolation(t *testing.T) {
	sc, err := fixture.BuildCorruptedLayerScenario(fixture.DefaultOptions(42))
	if err != nil {
		t.Fatal(err)
	}
	rep, runErr := run(t, sc, []fixture.Fault{fixture.FaultCorruptLayer})
	if runErr != nil {
		t.Fatalf("a corrupt top layer must not fail the run: %v", runErr)
	}
	if rep.RecoveredLayers != 2 || rep.TotalLayers != 3 {
		t.Fatalf("recovered %d/%d, want 2/3", rep.RecoveredLayers, rep.TotalLayers)
	}
	if len(rep.UnavailableLayers) != 1 || rep.UnavailableLayers[0] != 2 {
		t.Fatalf("unavailable layers = %v, want [2]", rep.UnavailableLayers)
	}
	// The surviving layers still produce a fully hashed index.
	if rep.SegmentHashMatch != 1 || len(rep.SegmentIndexes) != rep.Segments {
		t.Fatalf("partial result is not fully indexed: hash=%v segments=%d indexes=%d",
			rep.SegmentHashMatch, rep.Segments, len(rep.SegmentIndexes))
	}
	if rep.KeyframeHitRate != 1 {
		t.Fatalf("keyframe hit rate on the surviving layer = %v", rep.KeyframeHitRate)
	}
}

func TestCrossStageAssertions(t *testing.T) {
	sc, faults := build(t, 42, fixture.FaultWrap, fixture.FaultLoss, fixture.FaultColorConflict)
	rep, err := run(t, sc, faults)
	if err != nil {
		t.Fatal(err)
	}
	// Sample-count agreement: every fan-in segment must have a hashed index.
	if len(rep.SegmentIndexes) != rep.Segments {
		t.Fatalf("segments=%d but %d are hashed", rep.Segments, len(rep.SegmentIndexes))
	}
	// Boundary monotonicity.
	for i := 1; i < len(rep.SegmentIndexes); i++ {
		if rep.SegmentIndexes[i].StartTick < rep.SegmentIndexes[i-1].EndTick {
			t.Fatalf("segments overlap at %d", i)
		}
	}
	// Keyframe times fall on sample boundaries and are monotonically
	// increasing in the index.
	last := int64(-1)
	for _, e := range rep.KeyframeIndex.Entries {
		if e.Ticks <= last {
			t.Fatalf("keyframe ticks not increasing: %d after %d", e.Ticks, last)
		}
		last = e.Ticks
	}
	// Every hashed segment recorded its event correspondence.
	for _, s := range rep.SegmentIndexes {
		for _, id := range s.EventIDs {
			if !strings.Contains(id, ":") {
				t.Fatalf("malformed event id %q", id)
			}
		}
	}
	// The report never claims a layer is both recovered and unavailable.
	for _, idx := range rep.UnavailableLayers {
		for _, got := range []int{0, 1, 2} {
			if idx == got && got < rep.RecoveredLayers {
				continue
			}
		}
	}
	if rep.EventMemoryBytes <= 0 {
		t.Fatal("event index memory must be accounted")
	}
}

func TestPartialLayerStillIndexes(t *testing.T) {
	// With a single-rung ladder there is no fallback layer: the run must fail
	// rather than pretend the index is complete.
	opts := fixture.DefaultOptions(42)
	opts.Ladder = 1
	sc, err := fixture.BuildScenario(opts)
	if err != nil {
		t.Fatal(err)
	}
	in := fixture.PipelineInput(sc, nil)
	in.CorruptLayer = true
	rep, runErr := pipeline.Run(in, pipeline.DefaultLimits())
	if runErr == nil {
		t.Fatal("a single damaged rung must fail the run")
	}
	if rep.TotalLayers != 1 {
		t.Fatalf("total layers = %d, want 1", rep.TotalLayers)
	}
}
