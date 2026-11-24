package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"archivecheck/internal/fixture"
	"archivecheck/internal/manifest"
	"archivecheck/internal/merr"
	"archivecheck/internal/pipeline"
)

// Exit code contract:
//
//	0 - completed with no blocking dependency
//	1 - processing failure or a blocked dependency
//	2 - invalid command line arguments
const (
	exitOK        = 0
	exitBlocked   = 1
	exitBadArgs   = 2
	defaultFixtmp = ".archive-fixture"
)

type cliArgs struct {
	scenario     string
	tracks       int
	bandwidthRaw string
	window       int
	out          string
	resume       string
	fixtureDir   string
	format       string
	maxAttempts  int
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	args, err := parseArgs(argv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archivecheck: "+err.Error())
		return exitBadArgs
	}
	bw, err := resolveBandwidths(args.bandwidthRaw, args.tracks)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archivecheck: "+err.Error())
		return exitBadArgs
	}

	fixtureRoot := args.fixtureDir
	generatedHere := false
	if fixtureRoot == "" {
		fixtureRoot = filepath.Join(args.out, defaultFixtmp)
		generatedHere = true
	}
	cfg := fixture.Config{
		Scenario: args.scenario, Tracks: args.tracks,
		BandwidthKbps: bw, Window: args.window,
	}
	if _, err := fixture.Generate(cfg, fixtureRoot); err != nil {
		return emitError(err)
	}

	result, err := pipeline.Run(pipeline.RunOptions{
		FixtureRoot: fixtureRoot,
		Format:      args.format,
		Window:      args.window,
		MaxAttempts: args.maxAttempts,
		OutDir:      args.out,
		ResumeDir:   args.resume,
	})
	if err != nil {
		return emitError(err)
	}
	if generatedHere {
		result.Scenario = args.scenario
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "archivecheck: cannot encode report: "+err.Error())
		return exitBlocked
	}
	fmt.Println(string(data))

	switch result.Status {
	case "completed":
		return exitOK
	default:
		// blocked or degraded-but-blocking: any blocking dep means exit 1
		if result.Blocking != nil || result.LostPackets > 0 {
			return exitBlocked
		}
		// truncated/degraded without blocking still completes the check
		// successfully (errors are fully accounted in the JSON report).
		return exitOK
	}
}

func parseArgs(argv []string) (cliArgs, error) {
	fs := flag.NewFlagSet("archivecheck", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var a cliArgs
	fs.StringVar(&a.scenario, "fixture", "", "fixture scenario: "+
		strings.Join(fixture.Scenarios, ", "))
	fs.IntVar(&a.tracks, "tracks", 3, "number of tracks (>=1)")
	fs.StringVar(&a.bandwidthRaw, "bandwidth-kbps", "",
		"comma-separated target bandwidth per track in kbps")
	fs.IntVar(&a.window, "window", 4, "bounded reorder window (>=1)")
	fs.StringVar(&a.out, "out", "", "output directory for the frame sequence")
	fs.StringVar(&a.resume, "resume", "", "previous output directory to resume from")
	fs.StringVar(&a.fixtureDir, "fixture-dir", "",
		"reuse an existing generated fixture root instead of regenerating")
	fs.StringVar(&a.format, "format", manifest.FormatHLS,
		"manifest format to parse: hls or dash")
	fs.IntVar(&a.maxAttempts, "max-attempts", 2, "idempotent retry attempts per node")
	if err := fs.Parse(argv); err != nil {
		return a, err
	}
	if a.scenario == "" {
		return a, merr.New(merr.KindArgument, "cli", "--fixture",
			"--fixture is required")
	}
	known := false
	for _, s := range fixture.Scenarios {
		if s == a.scenario {
			known = true
		}
	}
	if !known {
		return a, merr.New(merr.KindArgument, "cli", "--fixture",
			"unknown scenario: "+a.scenario)
	}
	if a.tracks < 1 {
		return a, merr.New(merr.KindArgument, "cli", "--tracks",
			"tracks must be >= 1")
	}
	if a.window < 1 {
		return a, merr.New(merr.KindArgument, "cli", "--window",
			"window must be >= 1")
	}
	if a.out == "" {
		return a, merr.New(merr.KindArgument, "cli", "--out",
			"--out is required")
	}
	if a.format != manifest.FormatHLS && a.format != manifest.FormatDASH {
		return a, merr.New(merr.KindArgument, "cli", "--format",
			"format must be hls or dash")
	}
	if a.maxAttempts < 1 {
		return a, merr.New(merr.KindArgument, "cli", "--max-attempts",
			"max-attempts must be >= 1")
	}
	return a, nil
}

// defaultBandwidthKbps is used when --bandwidth-kbps is omitted, so the
// burst-loss/repaired acceptance commands remain self-contained.
var defaultBandwidthKbps = []int{800, 420, 1100, 600, 300}

// resolveBandwidths returns the explicit per-track bandwidths, or a
// deterministic default list when the flag is omitted.
func resolveBandwidths(raw string, tracks int) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		if tracks > len(defaultBandwidthKbps) {
			return nil, merr.New(merr.KindArgument, "cli", "--bandwidth-kbps",
				"omit --bandwidth-kbps only for up to "+
					strconv.Itoa(len(defaultBandwidthKbps))+" tracks")
		}
		return append([]int(nil), defaultBandwidthKbps[:tracks]...), nil
	}
	return parseBandwidths(raw, tracks)
}

func parseBandwidths(raw string, tracks int) ([]int, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != tracks {
		return nil, merr.New(merr.KindArgument, "cli", "--bandwidth-kbps",
			fmt.Sprintf("expected %d bandwidth values, got %d", tracks, len(parts)))
	}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v <= 0 {
			return nil, merr.New(merr.KindArgument, "cli", "--bandwidth-kbps",
				"each bandwidth must be a positive integer: "+p)
		}
		out = append(out, v)
	}
	return out, nil
}

// emitError maps an internal error onto the documented exit codes and
// prints a locatable JSON error object to stderr.
func emitError(err error) int {
	me, ok := merr.As(err)
	if !ok {
		me = merr.Wrap(err, "cli", "-", "unexpected failure")
	}
	payload := map[string]interface{}{
		"status":    "error",
		"kind":      me.Kind,
		"subsystem": me.Subsystem,
		"location":  me.Location,
		"message":   me.Message,
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	fmt.Fprintln(os.Stderr, string(data))
	if me.Kind == merr.KindArgument {
		return exitBadArgs
	}
	return exitBlocked
}
