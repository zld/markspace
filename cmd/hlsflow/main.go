// Command hlsflow turns unreliable multi-rendition H.264/AAC MPEG-TS inputs
// into a verified HLS publishing directory. Subcommands:
//
//	hlsflow fixture --dir DIR --scenario clean|faults --rates R,R,R
//	hlsflow build   --job JOB --input DIR --output DIR [flags]
//	hlsflow verify  --job JOB --master FILE [--json]
//
// Exit codes: 0 clean, 1 verification/checksum failure, 2 bad arguments or
// unparseable configuration/input.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"hlsflow/internal/config"
	"hlsflow/internal/fixture"
	"hlsflow/internal/pipeline"
	"hlsflow/internal/safeio"
)

// Exit codes as specified in the acceptance contract.
const (
	exitOK     = 0
	exitVerify = 1
	exitUsage  = 2
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	var err error
	switch os.Args[1] {
	case "fixture":
		err = runFixture(os.Args[2:])
	case "build":
		err = runBuild(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(exitUsage)
	}
	if err != nil {
		if fe, ok := err.(*usageError); ok {
			fmt.Fprintln(os.Stderr, "error:", fe)
			os.Exit(exitUsage)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitUsage)
	}
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usage() {
	fmt.Fprint(os.Stderr, `hlsflow - local HLS packaging and verification

usage:
  hlsflow fixture --dir DIR [--scenario clean|faults] [--rates 800000,1800000,3500000]
  hlsflow build   --job JOB.json --input DIR --output DIR
                  [--target-duration 2s] [--gap-timeout 3s] [--finalize]
  hlsflow verify  --job JOB.json --master DIR/master.m3u8 [--json]

exit codes: 0 no issues, 1 verification failure, 2 bad arguments/config/input
`)
}

func parseRates(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, &usageError{msg: "invalid rate " + p}
		}
		out = append(out, n)
	}
	return out, nil
}

func runFixture(args []string) error {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	dir := fs.String("dir", "", "output directory for the fixture tree")
	scenario := fs.String("scenario", fixture.ScenarioClean, "clean or faults")
	ratesFlag := fs.String("rates", "", "comma-separated bitrates")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: err.Error()}
	}
	if *dir == "" {
		return &usageError{msg: "fixture requires --dir"}
	}
	if *scenario != fixture.ScenarioClean && *scenario != fixture.ScenarioFaults {
		return &usageError{msg: "scenario must be clean or faults"}
	}
	rates, err := parseRates(*ratesFlag)
	if err != nil {
		return err
	}
	res, err := fixture.Generate(*dir, *scenario, rates)
	if err != nil {
		return err
	}
	fmt.Printf("fixture: wrote %d renditions to %s (scenario=%s)\n",
		len(res.Renditions), res.InputRoot, *scenario)
	for _, rd := range res.Renditions {
		fmt.Printf("  rendition=%s bitrate=%d segments=%d\n",
			rd.Name, rd.Bitrate, rd.ExpectedCount())
	}
	return nil
}

func runBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	jobPath := fs.String("job", "", "path to job.json")
	input := fs.String("input", "", "input directory holding per-rendition inboxes")
	output := fs.String("output", "", "publishing output directory")
	target := fs.Duration("target-duration", 2*time.Second, "target segment duration")
	gap := fs.Duration("gap-timeout", 0, "how long to wait for a missing source segment")
	finalize := fs.Bool("finalize", false, "close playlists with EXT-X-ENDLIST (VOD)")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: err.Error()}
	}
	if *jobPath == "" || *input == "" || *output == "" {
		return &usageError{msg: "build requires --job, --input and --output"}
	}
	job, err := config.Load(*jobPath)
	if err != nil {
		return err
	}
	inputRoot, err := safeio.NewRoot(*input)
	if err != nil {
		return err
	}
	outputRoot, err := safeio.EnsureRoot(*output)
	if err != nil {
		return err
	}
	report, err := pipeline.Build(job, inputRoot, outputRoot, pipeline.Options{
		JobPath: *jobPath, InputDir: *input, OutputDir: *output,
		TargetDuration: *target, GapTimeout: *gap, Finalize: *finalize,
	})
	if err != nil {
		return err
	}
	renderText(report)
	return nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	jobPath := fs.String("job", "", "path to job.json")
	master := fs.String("master", "", "path to the published master.m3u8")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return &usageError{msg: err.Error()}
	}
	if *jobPath == "" || *master == "" {
		return &usageError{msg: "verify requires --job and --master"}
	}
	job, err := config.Load(*jobPath)
	if err != nil {
		return err
	}
	// Confine verification to the directory that contains the master playlist.
	outDir := filepathDir(*master)
	outputRoot, err := safeio.NewRoot(outDir)
	if err != nil {
		return err
	}
	report, err := pipeline.Verify(job, outputRoot)
	if err != nil {
		return err
	}
	if *asJSON {
		if err := renderJSON(report); err != nil {
			return err
		}
	} else {
		renderText(report)
	}
	if report.HasFailures() {
		os.Exit(exitVerify)
	}
	return nil
}
