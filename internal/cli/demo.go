package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediasync/internal/engine"
	"mediasync/internal/fixture"
	"mediasync/internal/output"
)

// Demo generates a seeded capture fixture, runs the full kernel and prints a
// machine-readable summary.
func Demo(args []string) (int, error) {
	fs := newFlagSet("demo", os.Stderr)
	seed := fs.Int64("seed", 17, "fixture seed; identical seeds produce identical output")
	wrapBits := fs.Uint("wrap-bits", 16, "RTP sequence wrap width in bits")
	driftPPM := fs.Float64("drift-ppm", 120, "audio clock drift injected into the fixture")
	lateMS := fs.Float64("late-window-ms", 500, "late-arrival window in milliseconds")
	workdir := fs.String("workdir", "", "working directory (default: ./mediasync-work)")
	durationMS := fs.Int64("duration-ms", 20_000, "captured media duration in milliseconds")
	cameras := fs.Int("cameras", 3, "number of video cameras")
	quiet := fs.Bool("quiet", false, "suppress progress messages")
	if err := parseArgs(fs, args); err != nil {
		return output.ExitFailure, err
	}
	wd, err := resolveWorkDir(*workdir)
	if err != nil {
		return output.ExitFailure, err
	}
	runDir := filepath.Join(wd, "demo")
	capture := fixture.DefaultCaptureConfig(*seed)
	capture.WrapBits = *wrapBits
	capture.DriftPPM = *driftPPM
	capture.DurationMS = *durationMS
	capture.Cameras = *cameras
	manPath, evPath, err := fixture.Generate(runDir, capture)
	if err != nil {
		return output.ExitFailure, fmt.Errorf("generate fixture: %w", err)
	}
	in, err := engine.LoadInput(manPath, evPath)
	if err != nil {
		return output.ExitFailure, err
	}
	// --drift-ppm is the drift injected into the fixture. The correction
	// bound is deliberately wider, so a correct estimate is never clamped and
	// a clamped result would be a genuine finding.
	opts := engine.Options{
		WorkDir: filepath.Join(runDir, "checkpoints"), Seed: *seed,
		WrapBits: *wrapBits, DriftPPM: 1000, LateWindowMS: *lateMS,
	}
	eng, err := engine.New(opts, in.Manifest)
	if err != nil {
		return output.ExitFailure, err
	}
	res, err := eng.Run(in)
	if err != nil {
		return output.ExitFailure, err
	}
	res.Summary.Command = "demo"
	res.Summary.Seed = *seed
	res.Summary.Fixture = "generated"
	res.Summary.ExitCode = output.ExitOK
	res.Summary.ErrorCount = len(res.Errors)
	if len(res.Errors) > 0 {
		res.Summary.OK = false
		res.Summary.ExitCode = output.ExitFailure
		res.Summary.Messages = errorMessages(res.Errors)
	}
	if err := printSummary(os.Stdout, res.Summary); err != nil {
		return output.ExitFailure, err
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "demo: %d packets, %d records, digest %s\n",
			res.Summary.PacketTotal, len(res.Records), res.Summary.Digest)
	}
	if !res.Summary.OK {
		return output.ExitFailure, nil
	}
	if res.Summary.MaxAVSkewMS > 20 {
		res.Summary.Messages = append(res.Summary.Messages, fmt.Sprintf("max A/V skew %.3f ms exceeds 20 ms budget", res.Summary.MaxAVSkewMS))
		return output.ExitFailure, fmt.Errorf("max A/V skew %.3f ms exceeds 20 ms", res.Summary.MaxAVSkewMS)
	}
	return output.ExitOK, nil
}

func resolveWorkDir(explicit string) (string, error) {
	dir := explicit
	if dir == "" {
		dir = "mediasync-work"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create work directory %q: %w", dir, err)
	}
	return dir, nil
}

func printSummary(w io.Writer, s output.Summary) error {
	return output.WriteJSON(w, s)
}

func errorMessages(errs interface{ Error() string }) []string {
	return []string{errs.Error()}
}
