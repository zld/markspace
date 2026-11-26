package main

import (
	"fmt"
	"os"
	"path/filepath"

	"mediad/internal/fixture"
	"mediad/internal/job"
	"mediad/internal/media"
	"mediad/internal/plan"
)

// corruptOutcome records how one malformed input was rejected.
type corruptOutcome struct {
	name   string
	stage  media.Stage
	offset int64
	exit   int
}

// verifyCorruptInputs generates truncated, zero-timescale and out-of-bounds
// transition fixtures and asserts each is rejected with a locatable error
// and never reaches a complete manifest.
func (d *demoEnv) verifyCorruptInputs() ([]corruptOutcome, error) {
	out := make([]corruptOutcome, 0, 3)

	// 1. Truncated MP4.
	truncPath := filepath.Join(d.inputDir, "truncated.mp4")
	validPath := filepath.Join(d.inputDir, "truncated-src.mp4")
	spec := fixture.MP4Spec{
		Path: validPath, Timescale: 1000,
		SampleDurs: []uint32{1000, 1000, 1000, 1000},
		Payload: [][]byte{
			payloadBytes(d.flags.seed, "t", 0, 64),
			payloadBytes(d.flags.seed, "t", 1, 64),
			payloadBytes(d.flags.seed, "t", 2, 64),
			payloadBytes(d.flags.seed, "t", 3, 64),
		},
	}
	if err := fixture.WriteTruncatedMP4(spec, truncPath); err != nil {
		return nil, err
	}
	if _, err := media.ParseMP4(truncPath, "broken-video"); err == nil {
		return nil, fmt.Errorf("truncated MP4 was accepted")
	} else {
		co, ok := toOutcome("truncated-mp4", err)
		if !ok {
			return nil, fmt.Errorf("truncated MP4 produced unstructured error: %w", err)
		}
		out = append(out, co)
	}

	// 2. Zero-timescale MP4.
	zeroPath := filepath.Join(d.inputDir, "zero-ts.mp4")
	if err := fixture.WriteZeroTimescaleMP4(zeroPath, [][]byte{
		payloadBytes(d.flags.seed, "z", 0, 32),
		payloadBytes(d.flags.seed, "z", 1, 32),
	}); err != nil {
		return nil, err
	}
	if _, err := media.ParseMP4(zeroPath, "zero-video"); err == nil {
		return nil, fmt.Errorf("zero-timescale MP4 was accepted")
	} else {
		co, _ := toOutcome("zero-timescale", err)
		out = append(out, co)
	}

	// 3. Out-of-bounds cross-track transition.
	oobErr := d.verifyOOBTransition()
	if oobErr == nil {
		return nil, fmt.Errorf("out-of-bounds transition was accepted")
	}
	co, ok := toOutcome("oob-transition", oobErr)
	if !ok {
		return nil, fmt.Errorf("OOB transition produced unstructured error: %w", oobErr)
	}
	out = append(out, co)
	return out, nil
}

// verifyOOBTransition builds a valid timeline then attaches a transition
// whose end exceeds the destination track's content, returning the
// structured, byte-addressable error produced by validation.
func (d *demoEnv) verifyOOBTransition() error {
	tb, _ := media.NewTimeBase(1000)
	video := &media.Track{Key: "video", TimeBase: tb}
	audio := &media.Track{Key: "audio", TimeBase: tb}
	for i := 0; i < 4; i++ {
		video.Samples = append(video.Samples, media.Sample{
			Index: i + 1, StartNs: int64(i) * media.NanoPerSecond,
			DurationNs: media.NanoPerSecond, Size: 16,
		})
		audio.Samples = append(audio.Samples, media.Sample{
			Index: i + 1, StartNs: int64(i) * media.NanoPerSecond,
			DurationNs: media.NanoPerSecond, Size: 16,
		})
	}
	tl := media.NewTimeline([]*media.Track{video, audio})
	tl.Transitions = []media.Transition{{
		FromTrack: "video", ToTrack: "audio",
		Interval: media.Interval{StartNs: media.NanoPerSecond, EndNs: 99 * media.NanoPerSecond},
	}}
	return tl.ValidateTransitions()
}

func toOutcome(name string, err error) (corruptOutcome, bool) {
	me, ok := media.AsError(err)
	if !ok {
		return corruptOutcome{}, false
	}
	return corruptOutcome{
		name: name, stage: me.Stage, offset: me.InputOffset,
		exit: media.ClassifyExit(err),
	}, true
}

// report validates every acceptance invariant and prints the stable
// key=value summary. It returns 0 only when all expectations hold.
func (d *demoEnv) report(tl *media.Timeline, decision *plan.Decision,
	first, second, third *job.RunResult, corrupt []corruptOutcome) int {

	var failures []string
	check := func(cond bool, msg string) {
		if !cond {
			failures = append(failures, msg)
		}
	}

	check(tl.SampleCount() == 8, fmt.Sprintf("samples=%d want 8", tl.SampleCount()))
	check(len(tl.Tracks) == 2, fmt.Sprintf("tracks=%d want 2", len(tl.Tracks)))
	check(len(tl.Clips) == 1, "expected one clip")
	check(len(tl.Transitions) == 1, "expected one transition")

	check(decision.Width == 960 && decision.Height == 540,
		fmt.Sprintf("selected=%dx%d want 960x540", decision.Width, decision.Height))
	check(decision.Bitrate == d.flags.maxBitrate,
		fmt.Sprintf("bitrate=%d want %d", decision.Bitrate, d.flags.maxBitrate))
	check(plan.QualityFloat(decision.Quality) == 0.25,
		fmt.Sprintf("quality=%v want 0.25", decision.Quality))
	check(decision.Coverage.Num == 1 && decision.Coverage.Den == 1,
		fmt.Sprintf("coverage=%v want 1", decision.Coverage))

	check(first.Paused, "first run should pause")
	check(second.ResumedFromSample == 4,
		fmt.Sprintf("resumedFromSample=%d want 4", second.ResumedFromSample))
	// The completed re-run begins at the final checkpoint, so its loop never
	// executes and no object is written: duplicate writes must be zero.
	check(third.ResumedFromSample == 8 && third.DuplicateWrites == 0,
		fmt.Sprintf("duplicateWrites=%d want 0 (resumedFrom=%d)",
			third.DuplicateWrites, third.ResumedFromSample))
	check(second.ManifestDigest == third.ManifestDigest,
		"manifest digest changed across duplicate runs")
	check(second.ManifestDigest != "", "resumed run must publish a manifest")

	// outputDiff=0: entries and digest are byte-stable across runs.
	outputDiff := 0
	if second.ManifestDigest != third.ManifestDigest ||
		len(second.Entries) != len(third.Entries) {
		outputDiff = 1
	}
	check(outputDiff == 0, "output diff detected between runs")

	check(len(corrupt) == 3, fmt.Sprintf("corrupt cases=%d want 3", len(corrupt)))
	for _, co := range corrupt {
		check(co.stage == media.StageScan, co.name+" not reported at scan stage")
		check(co.exit == media.ExitInvalidInput, co.name+" did not map to exit 2")
	}
	// A complete manifest digest must be a 64-hex SHA-256.
	check(len(third.ManifestDigest) == 64 && isHex(third.ManifestDigest),
		"manifest digest is not 64-char hex")

	// Emit the stable acceptance summary regardless of outcome.
	fmt.Printf("tracks=%d\n", len(tl.Tracks))
	fmt.Printf("samples=%d\n", tl.SampleCount())
	fmt.Printf("selected=%dx%d\n", decision.Width, decision.Height)
	fmt.Printf("bitrate=%d\n", decision.Bitrate)
	fmt.Printf("quality=%.2f\n", plan.QualityFloat(decision.Quality))
	fmt.Printf("coverage=%d\n", coverageOne(decision))
	fmt.Printf("resumedFromSample=%d\n", second.ResumedFromSample)
	fmt.Printf("duplicateWrites=%d\n", third.DuplicateWrites)
	fmt.Printf("outputDiff=%d\n", outputDiff)
	fmt.Printf("manifestDigest=%s\n", third.ManifestDigest)
	for _, co := range corrupt {
		fmt.Printf("rejected=%s stage=%s offset=%d exit=%d\n",
			co.name, co.stage, co.offset, co.exit)
	}

	if len(failures) > 0 {
		for _, msg := range failures {
			fmt.Fprintln(os.Stderr, "FAIL:", msg)
		}
		return media.ExitInvariantFailed
	}
	fmt.Println("demo=PASS")
	return media.ExitOK
}

func coverageOne(d *plan.Decision) int {
	if d.Coverage.Num == d.Coverage.Den {
		return 1
	}
	return 0
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
