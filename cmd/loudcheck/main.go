// loudcheck is an offline loudness and true-peak analyzer for WAV masters.
//
// Subcommands:
//
//	fixture  generate a deterministic test WAV
//	analyze  measure one WAV file
//	batch    measure a list of WAV files with bounded concurrency
//
// Exit codes: 0 = processed and passed, 1 = gate failed,
// 2 = bad arguments or unprocessable input, 3 = batch had file errors.
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"loudcheck/internal/audio"
	"loudcheck/internal/batch"
	"loudcheck/internal/measure"
	"loudcheck/internal/report"
)

const (
	exitOK       = 0
	exitGateFail = 1
	exitUsage    = 2
	exitFileErr  = 3
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	var rc int
	switch args[0] {
	case "fixture":
		rc = cmdFixture(args[1:])
	case "analyze":
		rc = cmdAnalyze(args[1:])
	case "batch":
		rc = cmdBatch(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", args[0])
		usage()
		rc = exitUsage
	}
	return rc
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  loudcheck fixture --out FILE [--layout mono|stereo|5.1] [--seconds N] [--sample-rate HZ] [--loudness LUFS]
  loudcheck analyze --input FILE [gates] [--weights W,...] [--json]
  loudcheck batch (--list FILE | FILE...) [gates] [--weights W,...] [--concurrency N] [--json]
gates: --integrated-min LUFS --integrated-max LUFS --short-term-max LUFS --true-peak-max DBTP`)
}

// gateFlags collects optional gate thresholds; nil means "not set".
type gateFlags struct {
	integratedMin, integratedMax, shortTermMax, truePeakMax *float64
}

func bindGates(fs *flag.FlagSet) *gateFlags {
	g := &gateFlags{}
	bind := func(name, usage string, dst **float64) {
		fs.Func(name, usage, func(s string) error {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("invalid %s value %q: must be finite", name, s)
			}
			*dst = &v
			return nil
		})
	}
	bind("integrated-min", "minimum integrated loudness (LUFS)", &g.integratedMin)
	bind("integrated-max", "maximum integrated loudness (LUFS)", &g.integratedMax)
	bind("short-term-max", "maximum short-term loudness (LUFS)", &g.shortTermMax)
	bind("true-peak-max", "maximum true peak (dBTP)", &g.truePeakMax)
	return g
}

func (g *gateFlags) gates() (report.Gates, error) {
	if g.integratedMin != nil && g.integratedMax != nil && *g.integratedMin > *g.integratedMax {
		return report.Gates{}, fmt.Errorf("--integrated-min (%.2f) exceeds --integrated-max (%.2f)",
			*g.integratedMin, *g.integratedMax)
	}
	return report.Gates{
		IntegratedMin: g.integratedMin, IntegratedMax: g.integratedMax,
		ShortTermMax: g.shortTermMax, TruePeakMax: g.truePeakMax,
	}, nil
}

// parseWeightsFlag validates a --weights override before any media is read.
func parseWeightsFlag(s string) ([]float64, error) {
	if s == "" {
		return nil, nil
	}
	return measure.ParseWeights(s)
}

func cmdFixture(args []string) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	out := fs.String("out", "", "output WAV path (required)")
	layout := fs.String("layout", "stereo", "mono, stereo or 5.1")
	seconds := fs.Float64("seconds", 3, "duration in seconds")
	rate := fs.Int("sample-rate", 48000, "sample rate in Hz")
	loudness := fs.Float64("loudness", -20, "approximate integrated loudness (LUFS)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "fixture: --out is required")
		return exitUsage
	}
	err := audio.WriteFixture(*out, audio.FixtureOptions{
		Layout: *layout, Seconds: *seconds, SampleRate: *rate, TargetLoudness: *loudness,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
		return exitUsage
	}
	fmt.Printf("wrote %s (%s, %.3gs, %d Hz, target %.1f LUFS)\n",
		*out, *layout, *seconds, *rate, *loudness)
	return exitOK
}

func cmdAnalyze(args []string) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	input := fs.String("input", "", "WAV file to analyze (required)")
	jsonOut := fs.Bool("json", false, "emit JSON instead of a table")
	weightsFlag := fs.String("weights", "", "comma-separated per-channel loudness weights")
	gf := bindGates(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *input == "" {
		fmt.Fprintln(os.Stderr, "analyze: --input is required")
		return exitUsage
	}
	gates, err := gf.gates()
	if err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		return exitUsage
	}
	weights, err := parseWeightsFlag(*weightsFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		return exitUsage
	}
	rep := batch.AnalyzeFile(*input, batch.Options{Concurrency: 1, Gates: gates, Weights: weights})
	if *jsonOut {
		report.WriteJSON(os.Stdout, rep)
	} else {
		report.PrintTable(os.Stdout, []report.FileReport{rep}, report.Summary{})
	}
	if rep.Error != "" {
		fmt.Fprintln(os.Stderr, "analyze:", rep.Error)
		return exitUsage
	}
	if rep.OverallPass != nil && !*rep.OverallPass {
		return exitGateFail
	}
	return exitOK
}

func cmdBatch(args []string) int {
	fs := flag.NewFlagSet("batch", flag.ContinueOnError)
	list := fs.String("list", "", "file with one WAV path per line")
	conc := fs.Int("concurrency", 4, "max files processed in parallel")
	jsonOut := fs.Bool("json", false, "emit JSON instead of a table")
	weightsFlag := fs.String("weights", "", "comma-separated per-channel loudness weights")
	gf := bindGates(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	gates, err := gf.gates()
	if err != nil {
		fmt.Fprintln(os.Stderr, "batch:", err)
		return exitUsage
	}
	weights, err := parseWeightsFlag(*weightsFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "batch:", err)
		return exitUsage
	}
	if *conc < 1 {
		fmt.Fprintln(os.Stderr, "batch: --concurrency must be >= 1")
		return exitUsage
	}
	paths := fs.Args()
	if *list != "" {
		b, err := os.ReadFile(*list)
		if err != nil {
			fmt.Fprintln(os.Stderr, "batch: reading list:", err)
			return exitUsage
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				paths = append(paths, line)
			}
		}
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "batch: no input files (use --list or positional paths)")
		return exitUsage
	}
	reps, sum := batch.Run(paths, batch.Options{
		Concurrency: *conc, Gates: gates, Weights: weights,
	})
	if *jsonOut {
		report.WriteBatchJSON(os.Stdout, reps, sum)
	} else {
		report.PrintTable(os.Stdout, reps, sum)
	}
	if sum.Failed > 0 {
		return exitFileErr
	}
	if sum.GateFailed > 0 {
		return exitGateFail
	}
	return exitOK
}
