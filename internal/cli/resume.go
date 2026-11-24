package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"mediasync/internal/engine"
	"mediasync/internal/fixture"
	"mediasync/internal/output"
)

// Resume runs a capture twice: once interrupted after the requested event,
// then resumed from the last checkpoint. It reports both digests and the
// resume latency; a digest mismatch fails the command.
func Resume(args []string) (int, error) {
	fs := newFlagSet("resume", os.Stderr)
	name := fs.String("fixture", "generated-long", "fixture name (generated-long) or path to a capture directory")
	every := fs.Int64("checkpoint-every", 1000, "write a checkpoint every N events")
	interrupt := fs.Int64("interrupt-after", 2500, "stop the first run after N events")
	seed := fs.Int64("seed", 17, "fixture seed")
	wrapBits := fs.Uint("wrap-bits", 16, "RTP sequence wrap width in bits")
	driftPPM := fs.Float64("drift-ppm", 120, "audio drift in ppm")
	lateMS := fs.Float64("late-window-ms", 500, "late window in milliseconds")
	workdir := fs.String("workdir", "", "working directory (default: ./mediasync-work)")
	if err := parseArgs(fs, args); err != nil {
		return output.ExitFailure, err
	}
	if *every <= 0 {
		return output.ExitFailure, fmt.Errorf("--checkpoint-every must be positive")
	}
	if *interrupt <= 0 {
		return output.ExitFailure, fmt.Errorf("--interrupt-after must be positive")
	}
	wd, err := resolveWorkDir(*workdir)
	if err != nil {
		return output.ExitFailure, err
	}
	runDir := filepath.Join(wd, "resume")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return output.ExitFailure, err
	}
	manPath, evPath, err := captureFor(*name, runDir, *seed, *wrapBits, *driftPPM)
	if err != nil {
		return output.ExitFailure, err
	}
	in, err := engine.LoadInput(manPath, evPath)
	if err != nil {
		return output.ExitFailure, err
	}
	base := engine.Options{
		WorkDir: filepath.Join(runDir, "checkpoints"), Seed: *seed,
		WrapBits: *wrapBits, DriftPPM: 1000, LateWindowMS: *lateMS,
		CheckpointEvery: *every, InterruptAfter: *interrupt,
	}
	// Reference: a full, uninterrupted run with otherwise identical
	// parameters. The reference must not inherit the interrupt point.
	refOpts := base
	refOpts.InterruptAfter = 0
	refEng, err := engine.New(refOpts, in.Manifest)
	if err != nil {
		return output.ExitFailure, err
	}
	ref, err := refEng.Run(in)
	if err != nil {
		return output.ExitFailure, err
	}
	// Interrupted run: writes checkpoints then stops.
	intEng, err := engine.New(base, in.Manifest)
	if err != nil {
		return output.ExitFailure, err
	}
	first, err := intEng.Run(in)
	if err != nil {
		return output.ExitFailure, err
	}
	// Resume with the same working directory so the checkpoint log is found.
	resEng, err := engine.New(base, in.Manifest)
	if err != nil {
		return output.ExitFailure, err
	}
	res, err := resEng.ResumeFromCheckpoint(in)
	if err != nil {
		return output.ExitFailure, err
	}
	sum := res.Summary
	sum.Command = "resume"
	sum.Fixture = *name
	sum.ResumedFrom = res.ResumedFrom
	sum.ResumeMS = res.ResumeMS
	sum.Digest = res.Digest
	sum.PacketsDup = ref.Summary.PacketsDup
	sum.PacketTotal = ref.Summary.PacketTotal
	sum.Lost = ref.Summary.Lost
	sum.Recovered = ref.Summary.Recovered
	sum.LateRecomputed = ref.Summary.LateRecomputed
	sum.MaxAVSkewMS = ref.Summary.MaxAVSkewMS
	sum.DriftPPM = ref.Summary.DriftPPM
	sum.DriftClamped = ref.Summary.DriftClamped
	sum.Checkpoints = first.CheckpointCount
	sum.ExitCode = output.ExitOK
	ok, msg := output.CheckpointVerify(res.Digest, ref.Digest)
	if !ok {
		sum.OK = false
		sum.ExitCode = output.ExitFailure
		sum.ErrorCount = 1
		sum.Messages = append(sum.Messages, msg)
	}
	sum.Messages = append(sum.Messages, fmt.Sprintf("reference_digest=%s", ref.Digest))
	if err := printSummary(os.Stdout, sum); err != nil {
		return output.ExitFailure, err
	}
	fmt.Fprintf(os.Stderr, "resume: from event %d in %.3f ms, digest %s (reference %s)\n",
		res.ResumedFrom, res.ResumeMS, res.Digest, ref.Digest)
	if !ok {
		return output.ExitFailure, fmt.Errorf("%s", msg)
	}
	return output.ExitOK, nil
}

// captureFor materializes the requested capture source. The bundled
// generated-long fixture is a seeded capture, so resume exercises the same
// pipeline as demo.
func captureFor(name, dir string, seed int64, wrapBits uint, driftPPM float64) (string, string, error) {
	cfg := fixture.DefaultCaptureConfig(seed)
	cfg.WrapBits = wrapBits
	cfg.DriftPPM = driftPPM
	return fixture.Generate(filepath.Join(dir, "capture"), cfg)
}
