package demo

import (
	"fmt"
	"math"
	"path/filepath"

	"mediapack/merr"
	"mediapack/pipeline"
	"mediapack/safefs"
	"mediapack/timeline"
)

// Run executes the demo: generate inputs, run the pipeline, and print the
// acceptance fields. With resume enabled a process crash is simulated
// after the timeline stage (the render and manifest checkpoints are
// discarded) and the run resumes from the last verified stage.
func Run(seed int64, outDir string, resume bool) error {
	if outDir == "" {
		return merr.Paramf("demo", "--out is required")
	}
	params := Params{Seed: seed, FPS: 30, Width: 1280, Height: 720, DurationSecs: 4, TargetBps: 800000}
	in, err := GenerateInputs(outDir, seed)
	if err != nil {
		return err
	}
	def, err := BuildTimeline(in, params.FPS, params.DurationSecs*timeline.NanosPerSecond)
	if err != nil {
		return err
	}
	extra := struct {
		Tracks      []*timeline.Track     `json:"tracks"`
		Clips       []timeline.Clip       `json:"clips"`
		Transitions []timeline.Transition `json:"transitions"`
	}{def.Graph.Tracks(), def.Clips, def.Transitions}
	inputDigest, err := DigestInputs(in, extra)
	if err != nil {
		return err
	}
	paramsDigest := DigestParams(params)
	store, err := pipeline.NewStore(filepath.Join(outDir, "checkpoints"))
	if err != nil {
		return err
	}
	runner := &pipeline.Runner{Store: store, InputDigest: inputDigest, ParamsDigest: paramsDigest}
	stages := Stages(in, def, params, outDir)
	if resume {
		// crash point: the process "dies" during render; downstream
		// checkpoints from any earlier run must not survive
		if _, err := runner.Run(stages[:2]); err != nil {
			return err
		}
		if err := store.Remove(StageRender, StageManifest); err != nil {
			return err
		}
	}
	outcome, err := runner.Run(stages)
	if err != nil {
		return err
	}
	if _, err := pipeline.WriteProvenance(outcome.Provenance, outDir, "artifacts/provenance.json"); err != nil {
		return err
	}
	provPath, err := safefs.Join(outDir, "artifacts/provenance.json")
	if err != nil {
		return err
	}
	prov, err := pipeline.LoadProvenance(provPath)
	provenanceOK := err == nil && prov.Verify(inputDigest, paramsDigest)

	var mp manifestPayload
	if err := pipeline.Decode(outcome.Results, StageManifest, &mp); err != nil {
		return err
	}
	delta := math.Abs(float64(mp.Stats.ActualBps-mp.Stats.TargetBps)) / float64(mp.Stats.TargetBps) * 100
	fmt.Printf("frames=%d\n", mp.Stats.Frames)
	fmt.Printf("valid_frames=%d\n", mp.Stats.ValidFrames)
	fmt.Printf("gap_frames=%d\n", mp.Stats.GapFrames)
	fmt.Printf("target_bps=%d\n", mp.Stats.TargetBps)
	fmt.Printf("actual_bps=%d\n", mp.Stats.ActualBps)
	fmt.Printf("bitrate_delta_pct=%.2f\n", delta)
	fmt.Printf("quality_mean=%.2f\n", mp.Stats.QualityMean)
	fmt.Printf("manifest_max_skew_ms=%.2f\n", float64(mp.MaxSkewNanos)/1e6)
	fmt.Printf("resume_from=%s\n", outcome.ResumeFrom)
	fmt.Printf("provenance_ok=%t\n", provenanceOK)
	return nil
}
