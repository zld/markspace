package main

import (
	"path/filepath"

	"mediaforensics/internal/clock"
	"mediaforensics/internal/fixture"
	"mediaforensics/internal/manifest"
	"mediaforensics/internal/model"
	"mediaforensics/internal/parse"
)

// runClockOverflowCase parses an intact container then injects a clip start
// at MaxInt64 seconds. The rational builder must block the affected track
// while preserving its first source offset, and the run still completes with
// full evidence (exit 2: damage recovered per policy).
func runClockOverflowCase(root string, seed int64, frames int) (caseResult, error) {
	dir := filepath.Join(root, fixture.CaseClockOverflow)
	data, _, err := fixture.GenerateMP4(fixture.DefaultSpec(seed, frames))
	if err != nil {
		return caseResult{}, err
	}
	w, err := manifest.NewWriter(dir)
	if err != nil {
		return caseResult{}, err
	}
	res, perr := parse.Parse(data)
	if perr != nil {
		return caseResult{}, perr
	}

	// Inject an overflowing edit into the audio track's only clip.
	var blocked *model.Track
	for _, tr := range res.Timeline.Tracks {
		if tr.Kind == model.KindAudio {
			blocked = tr
			for i := range tr.Clips {
				tr.Clips[i].TrackStart = clock.New(1<<63-1, 1)
			}
		}
	}
	// Re-run the rational builder so the overflow is detected and the track
	// is blocked with its first source offset preserved.
	rebuilt := model.Build(res.Timeline.Tracks)
	res.Timeline = rebuilt

	stage := manifest.StageRecord{
		Name: manifest.StageParse, Status: statusFor(len(rebuilt.Errors)),
		Errors: rebuilt.Errors, Detail: "clock overflow injected into audio clip",
	}
	if _, err := w.WriteStage(stage); err != nil {
		return caseResult{}, err
	}
	offset := int64(-1)
	if len(rebuilt.Errors) > 0 {
		offset = rebuilt.Errors[0].Offset
	} else if blocked != nil {
		offset = blocked.EarliestOffset
	}
	rm := manifest.RunManifest{
		Seed: seed, Case: fixture.CaseClockOverflow,
		Stages:       []manifest.StageRecord{stage},
		ErrorSummary: manifest.ErrorDigest(rebuilt.Errors), ExitCode: 2,
	}
	_, sha, err := w.WriteRun(rm)
	if err != nil {
		return caseResult{}, err
	}
	return caseResult{
		name: fixture.CaseClockOverflow, offset: offset,
		recoveredRate: 1.0, syncErrorMs: 0, p95LatencyMs: 0,
		exitCode: 2, manifestSHA: sha,
	}, nil
}
