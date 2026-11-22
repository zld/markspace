// Command mediatimeline runs the deterministic media timeline demo.
//
//	mediatimeline demo --seed 42 --faults wrap,loss --ladder 3 --segment-ms 2000
//
// The command is local-only: it synthesizes its inputs from the seed, runs the
// pipeline in memory and prints the acceptance metrics to stdout. Exit code 0
// means the run passed, 2 means an unrecoverable input or boundary failure,
// and 1 means a parameter or runtime error.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"example.com/mediatimeline/internal/fixture"
	"example.com/mediatimeline/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

func run(args []string, stdout *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, "usage: mediatimeline demo [flags]")
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout)
	default:
		fmt.Fprintf(stdout, "unknown command %q\n", args[0])
		return 1
	}
}

type demoFlags struct {
	seed      int64
	faults    string
	ladder    int
	segmentMS int64
	frames    int
	jsonOut   bool
}

func runDemo(args []string, stdout *os.File) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stdout)
	var df demoFlags
	fs.Int64Var(&df.seed, "seed", 42, "deterministic generation seed")
	fs.StringVar(&df.faults, "faults", "", "comma-separated faults: wrap,loss,backward,color-conflict,audio-gap,audio-overlap,audio-loss,audio-short,corrupt-layer")
	fs.IntVar(&df.ladder, "ladder", 3, "number of ladder layers")
	fs.Int64Var(&df.segmentMS, "segment-ms", 2000, "segment duration in milliseconds")
	fs.IntVar(&df.frames, "frames", 90, "number of video frames")
	fs.BoolVar(&df.jsonOut, "json", false, "print the machine-readable report")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if err := df.validate(); err != nil {
		fmt.Fprintf(stdout, "error: %v\n", err)
		return 1
	}
	opts, faultList, err := df.scenario()
	if err != nil {
		fmt.Fprintf(stdout, "error: %v\n", err)
		return 1
	}
	scenario, err := fixture.BuildScenario(opts)
	if err != nil {
		fmt.Fprintf(stdout, "error: %v\n", err)
		return 1
	}
	in := fixture.PipelineInput(scenario, faultList)
	limits := pipeline.DefaultLimits()
	rep, runErr := pipeline.Run(in, limits)
	if df.jsonOut {
		if err := printJSON(stdout, rep, runErr); err != nil {
			fmt.Fprintf(stdout, "error: %v\n", err)
			return 1
		}
	} else {
		printReport(stdout, rep, runErr, df, faultList)
	}
	return pipeline.ExitCode(runErr)
}

func (d demoFlags) validate() error {
	if d.seed < 0 {
		return fmt.Errorf("seed must not be negative")
	}
	if d.ladder < 1 || d.ladder > 8 {
		return fmt.Errorf("ladder must be in [1,8], got %d", d.ladder)
	}
	if d.segmentMS < 100 || d.segmentMS > 60000 {
		return fmt.Errorf("segment-ms must be in [100,60000], got %d", d.segmentMS)
	}
	if d.frames < 1 || d.frames > 100000 {
		return fmt.Errorf("frames must be in [1,100000], got %d", d.frames)
	}
	for _, f := range splitFaults(d.faults) {
		if !knownFault(f) {
			return fmt.Errorf("unknown fault %q", f)
		}
	}
	return nil
}

func knownFault(s string) bool {
	switch fixture.Fault(s) {
	case fixture.FaultWrap, fixture.FaultLoss, fixture.FaultBackward, fixture.FaultColorConflict,
		fixture.FaultAudioGap, fixture.FaultAudioOverlap, fixture.FaultAudioLoss,
		fixture.FaultAudioShort, fixture.FaultCorruptLayer:
		return true
	}
	return false
}

func splitFaults(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (d demoFlags) scenario() (fixture.ScenarioOptions, []fixture.Fault, error) {
	opts := fixture.DefaultOptions(d.seed)
	opts.Ladder = d.ladder
	opts.SegmentMS = d.segmentMS
	opts.Frames = d.frames
	var list []fixture.Fault
	for _, s := range splitFaults(d.faults) {
		f := fixture.Fault(s)
		opts.Faults[f] = true
		list = append(list, f)
	}
	return opts, list, nil
}

// parseInt is kept for symmetry with the JSON diff tooling; the CLI itself
// uses flag.Int64Var.
func parseInt(s string, def int64) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}
