package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mediadist/demux"
	"mediadist/pipeline"
	"mediadist/segment"
	"mediadist/timeline"
)

// runDemo generates fixtures and runs the full pipeline into --out.
func runDemo(args []string) error {
	da, err := parseDemoFlags(args)
	if err != nil {
		return usageError(err)
	}
	if err := ensureWritableDir(da.out); err != nil {
		return err
	}
	seconds := int(da.duration.Seconds() + 0.5)
	inputs := make([]pipeline.Input, 0, len(da.bitrates))
	for i, bps := range da.bitrates {
		spec := demux.FixtureSpec{
			Seed:     da.seed,
			Duration: uint32(seconds),
			Video:    buildVideoSpec(da.seed, seconds, bps, da.faultCase),
			Audio:    buildAudioSpec(seconds, da.faultCase),
			Fault:    da.faultCase,
		}
		data, err := demux.WriteFixture(spec)
		if err != nil {
			return internalError(err)
		}
		inputs = append(inputs, pipeline.Input{
			ID:               bitrateID(i, bps),
			TargetBitrateBps: bps,
			Fixture:          data,
			GraphFault:       graphFault(da.faultCase),
		})
	}
	opts := pipeline.Options{
		OutputDir:       da.out,
		TargetAudioRate: 48000,
		ColorPriority:   colorPriorityToSources(da.colorPriority),
		Segment:         segment.DefaultConfig(),
	}
	// Persist the deterministic request so `recover` can rebuild clean inputs.
	if err := persistRequest(da.out, requestRecord{
		Seed: da.seed, DurationSec: seconds, Bitrates: da.bitrates,
		Case: string(da.faultCase),
	}); err != nil {
		return err
	}
	runner, err := pipeline.NewRunner(opts)
	if err != nil {
		return err
	}
	result, err := runner.Run(inputs)
	if err != nil {
		// The RunError already carries code, stage and locator; return it so
		// main's single error printer and exit-code classifier handle output.
		return err
	}
	printSummary(result, da.out)
	return nil
}

// runVerify reads a published manifest and re-checks every invariant.
func runVerify(args []string) error {
	dir, err := parseInputDir(args)
	if err != nil {
		return usageError(err)
	}
	manifestPath, err := safeJoin(dir, "manifest.json")
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return timeline.NewLocError(timeline.CodeBadArgument, timeline.StageManifest, 0, -1,
			"verify", fmt.Errorf("manifest not found under %q: %w", dir, err))
	}
	var m segment.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return timeline.NewLocError(timeline.CodeCheckpoint, timeline.StageManifest, 0, -1, "verify", err)
	}
	canonical, err := m.CanonicalJSON()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != m.SHA256 {
		return timeline.NewLocError(timeline.CodeCheckpoint, timeline.StageManifest, 0, -1,
			"verify", errors.New("manifest hash mismatch"))
	}
	cfg := segment.DefaultConfig()
	if m.AlignmentErrorTicks > cfg.MaxDurationErrorTicks {
		return timeline.NewLocError(timeline.CodeDurationConserv, timeline.StageManifest, 0, -1,
			"verify", fmt.Errorf("alignment %d > %d", m.AlignmentErrorTicks, cfg.MaxDurationErrorTicks))
	}
	printManifestVerification(&m)
	return nil
}

// runRecover takes a directory produced by a failing demo, resets the failed
// stage and reruns against repaired (clean) fixtures generated from the seed.
func runRecover(args []string) error {
	inDir, outDir, err := parseRecoverDirs(args)
	if err != nil {
		return usageError(err)
	}
	if err := ensureWritableDir(outDir); err != nil {
		return err
	}
	// The failed run left a checkpoint describing the failed stage.
	failedStore, err := pipeline.NewStore(filepath.Join(inDir, ".checkpoint"))
	if err != nil {
		return err
	}
	failedStage, failedState := failedStore.FailedStage()
	if failedStage == "" {
		return timeline.NewLocError(timeline.CodeBadArgument, timeline.StageManifest, 0, -1,
			"recover", errors.New("input directory has no failed stage to recover"))
	}
	recoveryCount := 0
	if demux.IsRecoverable(failedState.ErrorCode) {
		recoveryCount = 1
	} else {
		// Unrecoverable input: honor the exit-code contract rather than guessing.
		fmt.Fprintf(os.Stderr, "recover refused: fault %q is not recoverable\n", failedState.ErrorCode)
		return timeline.NewLocError(failedState.ErrorCode, failedStage, 0, -1, "recover",
			errors.New("fault cannot be repaired"))
	}

	// Rebuild clean fixtures from the same deterministic parameters. The demo
	// inputs are regenerated without fault injection, which is the "repaired
	// input" the acceptance flow expects.
	inputs, seed, bitrates, err := regenerateInputs(inDir)
	if err != nil {
		return err
	}
	opts := pipeline.Options{
		OutputDir:       outDir,
		TargetAudioRate: 48000,
		ColorPriority:   colorPriorityToSources("container"),
		Segment:         segment.DefaultConfig(),
	}
	runner, err := pipeline.NewRunner(opts)
	if err != nil {
		return err
	}
	if err := runner.Store().ResetFailed(); err != nil {
		return err
	}
	result, err := runner.Run(inputs)
	if err != nil {
		return err
	}
	printRecoverySummary(result, outDir, recoveryCount, failedStage, pipeline.StageDemux)
	_ = seed
	_ = bitrates
	return nil
}

// regenerateInputs reads the small request record persisted alongside the
// failing run, or falls back to the documented default 12 s/three-tier set.
func regenerateInputs(inDir string) ([]pipeline.Input, int64, []uint64, error) {
	recPath := filepath.Join(inDir, "request.json")
	raw, err := os.ReadFile(recPath)
	var rec requestRecord
	if err == nil {
		if jerr := json.Unmarshal(raw, &rec); jerr != nil {
			return nil, 0, nil, internalError(jerr)
		}
	} else {
		rec = requestRecord{Seed: 17, DurationSec: 12, Bitrates: []uint64{800_000, 1_600_000, 3_000_000}}
	}
	inputs := make([]pipeline.Input, 0, len(rec.Bitrates))
	for i, bps := range rec.Bitrates {
		spec := demux.FixtureSpec{
			Seed:     rec.Seed,
			Duration: uint32(rec.DurationSec),
			Video:    buildVideoSpec(rec.Seed, rec.DurationSec, bps, demux.FaultNone),
			Audio:    buildAudioSpec(rec.DurationSec, demux.FaultNone),
		}
		data, err := demux.WriteFixture(spec)
		if err != nil {
			return nil, 0, nil, internalError(err)
		}
		inputs = append(inputs, pipeline.Input{
			ID: bitrateID(i, bps), TargetBitrateBps: bps, Fixture: data,
		})
	}
	return inputs, rec.Seed, rec.Bitrates, nil
}

// requestRecord is the small persisted parameter file enabling deterministic
// regeneration during recovery.
type requestRecord struct {
	Seed        int64    `json:"seed"`
	DurationSec int      `json:"duration_sec"`
	Bitrates    []uint64 `json:"bitrates"`
	Case        string   `json:"case,omitempty"`
}

func graphFault(f demux.FaultCase) demux.FaultCase {
	switch f {
	case demux.FaultTimestampBack, demux.FaultMissingKeyframe:
		return f
	default:
		return demux.FaultNone
	}
}

func bitrateID(i int, bps uint64) string {
	return fmt.Sprintf("rep%d-%dk", i, bps/1000)
}

func usageError(err error) error {
	return pipeline.NewRunErrorForTest(timeline.CodeBadArgument, err)
}

func internalError(err error) error {
	return pipeline.NewRunErrorForTest("INTERNAL", err)
}
