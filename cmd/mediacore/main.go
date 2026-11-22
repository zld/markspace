// Command mediacore is the terminal entry point for the media kernel. It
// generates fixtures, runs the pipeline, and prints one acceptance line per
// run. Exit codes: 0 success, 1 internal error, 2 dependency cycle, 3 input
// that cannot be recovered.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"mediacore/internal/core"
	"mediacore/internal/fixture"
	"mediacore/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "demo":
		return demo(args[1:])
	case "fixtures":
		return fixtures(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "mediacore: unknown command %q\n", args[0])
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  mediacore demo [flags]          run the acceptance demo
  mediacore fixtures --dir DIR    write all generated fixtures

demo flags:
  --fixture NAME      clean|no-moov|truncated|bad-range|dep-cycle|wrap|no-index|garbled
  --sample-rate N     output sample rate (default 48000)
  --channels LAYOUT   mono|stereo|lcr|quad|5.1 (default stereo)
  --target-kbps N     target bitrate (default 128)
  --loudness LUFS     loudness target (default -16)
  --peak DBFS         true-peak ceiling (default -1)
  --out PATH          manifest output path
  --dir DIR           fixture cache directory (default: temp dir)
`)
}

func demo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	kind := fs.String("fixture", "clean", "fixture to run")
	rate := fs.Int("sample-rate", 48000, "output sample rate")
	channels := fs.String("channels", "stereo", "output channel layout")
	kbps := fs.Int("target-kbps", 128, "target bitrate in kbps")
	lufs := fs.Float64("loudness", -16, "loudness target in LUFS")
	peak := fs.Float64("peak", -1, "true-peak ceiling in dBFS")
	out := fs.String("out", "", "manifest output path")
	dir := fs.String("dir", "", "fixture cache directory")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	path, err := fixturePath(*dir, fixture.Kind(*kind))
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediacore: %v\n", err)
		if core.IsCode(err, core.CodeMissingTrack) || core.IsCode(err, core.CodeParamConflict) {
			return 3
		}
		return 1
	}

	res := pipeline.Run(pipeline.Options{
		File: path, SampleRate: *rate, Channels: *channels, TargetKbps: *kbps,
		LoudnessLUFS: *lufs, PeakDBFS: *peak, PacketMs: 20, OutPath: *out,
	})
	if res.Err != nil {
		fmt.Fprintf(os.Stderr, "mediacore: %v\n", res.Err)
		printStages(res)
		return res.ExitCode
	}
	printAcceptance(res)
	printStages(res)
	return 0
}

// printAcceptance prints the single line the acceptance script parses.
func printAcceptance(res *pipeline.Result) {
	m := res.Manifest
	gaps := len(m.Gaps)
	valid := m.Stats.OutputFrames
	status := "ok"
	if gaps > 0 {
		status = "recovered"
	}
	fmt.Printf(
		"status=%s target_kbps=%d actual_kbps=%.2f bitrate_error_pct=%.2f input_lufs=%.2f output_lufs=%.2f clipped_samples=%d gaps=%d manifest_alignment_ms=%.4f valid_samples=%d\n",
		status, m.TargetKbps, m.ActualKbps, m.BitrateErrorPct, m.InputLUFS, m.OutputLUFS,
		m.ClippedSamples, gaps, m.AlignmentMs, valid)
}

func printStages(res *pipeline.Result) {
	if len(res.Stages) == 0 {
		return
	}
	var parts []string
	for _, s := range res.Stages {
		state := "ok"
		if !s.OK {
			state = "fail"
		}
		parts = append(parts, fmt.Sprintf("%s:%s", s.Stage, state))
	}
	fmt.Printf("stages=%s\n", strings.Join(parts, ","))
	if res.Recovery != nil {
		fmt.Printf("index_source=%s gaps=%d warnings=%d\n",
			res.Recovery.IndexSource, len(res.Recovery.Gaps), len(res.Recovery.Warnings))
	}
}

func fixtures(args []string) int {
	fs := flag.NewFlagSet("fixtures", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *dir == "" {
		d, err := os.MkdirTemp("", "mediacore-fixtures-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "mediacore: %v\n", err)
			return 1
		}
		*dir = d
	}
	paths, err := fixture.Ensure(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mediacore: %v\n", err)
		return 1
	}
	for _, k := range fixture.Kinds {
		fmt.Printf("%s=%s\n", k, paths[k])
	}
	return 0
}

// fixturePath generates the requested fixture into a cache directory and
// returns its path. The file is rebuilt each run so repeated runs always read
// identical bytes.
func fixturePath(dir string, kind fixture.Kind) (string, error) {
	if !validKind(kind) {
		return "", core.New(core.CodeParamConflict, core.StageScan, false,
			fmt.Sprintf("unknown fixture %q", kind))
	}
	if dir == "" {
		d, err := os.MkdirTemp("", "mediacore-"+string(kind)+"-")
		if err != nil {
			return "", core.Wrap(core.CodeIO, core.StageScan, false, "create fixture dir", err)
		}
		dir = d
	} else {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", core.Wrap(core.CodeIO, core.StageScan, false, "create fixture dir", err)
		}
	}
	m := fixture.GeneratePCM(fixture.DefaultPlan(kind))
	return fixture.Write(dir, m)
}

func validKind(k fixture.Kind) bool {
	for _, x := range fixture.Kinds {
		if k == x {
			return true
		}
	}
	return false
}
