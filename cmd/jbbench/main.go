// Command jbbench runs the jitter-buffer benchmark. Subcommands:
//
//	jbbench simulate   generate an impaired offline stream and replay it
//	jbbench receive    listen on loopback UDP and run the live receive path
//	jbbench report     regenerate a scenario and print only the text summary
//
// Exit codes: 0 ok, 2 parameter error, 3 input/transport error,
// 4 internal consistency failure.
package main

import (
	"flag"
	"fmt"
	"os"

	"jbbench/internal/jitter"
	"jbbench/internal/metrics"
	"jbbench/internal/receive"
	"jbbench/internal/sim"
)

// Exit codes mandated by the benchmark contract.
const (
	exitOK          = 0
	exitParamError  = 2
	exitInputError  = 3
	exitConsistency = 4
)

type commonFlags struct {
	frames    int
	seed      int64
	lossBurst int
	lossRate  float64
	reorder   int
	duplicate float64
	corrupt   float64
	json      bool
	windowCap int
	dropEvict bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return exitParamError
	}
	switch args[0] {
	case "simulate", "report":
		return runSimulate(args[1:], args[0] == "simulate")
	case "receive":
		return runReceive(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "jbbench: unknown subcommand %q\n\n", args[0])
		printUsage(os.Stderr)
		return exitParamError
	}
}

func registerCommon(fs *flag.FlagSet, cf *commonFlags) {
	fs.IntVar(&cf.frames, "frames", 1000, "number of sender frames to generate")
	fs.Int64Var(&cf.seed, "seed", 1, "fixed RNG seed for replayable scenarios")
	fs.IntVar(&cf.lossBurst, "loss-burst", 4, "maximum burst loss length in frames")
	fs.Float64Var(&cf.lossRate, "loss-rate", 0.02, "probability a loss burst starts")
	fs.IntVar(&cf.reorder, "reorder", 8, "max reorder delay in frames (0 disables)")
	fs.Float64Var(&cf.duplicate, "duplicate", 0.02, "per-frame duplicate probability")
	fs.Float64Var(&cf.corrupt, "corrupt", 0.005, "per-frame corruption probability")
	fs.BoolVar(&cf.json, "json", false, "emit the full report as JSON")
	fs.IntVar(&cf.windowCap, "window", 64, "reorder window capacity in frames")
	fs.BoolVar(&cf.dropEvict, "evict-oldest", false,
		"evict the oldest frame on window overflow instead of rejecting")
}

func (cf commonFlags) toSimConfig() sim.Config {
	return sim.Config{
		Frames:        cf.frames,
		Seed:          cf.seed,
		LossBurst:     cf.lossBurst,
		LossRate:      cf.lossRate,
		ReorderWindow: cf.reorder,
		DuplicateRate: cf.duplicate,
		CorruptRate:   cf.corrupt,
	}
}

func (cf commonFlags) playerConfig() jitter.PlayerConfig {
	pc := jitter.DefaultPlayerConfig()
	pc.WindowCapacity = cf.windowCap
	if cf.dropEvict {
		pc.DropPolicy = jitter.DropEvictOld
	}
	return pc
}

func runSimulate(args []string, fullJSON bool) int {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf commonFlags
	registerCommon(fs, &cf)
	if err := fs.Parse(args); err != nil {
		return exitParamError
	}
	if err := validateCommon(cf); err != nil {
		fmt.Fprintf(os.Stderr, "jbbench: %v\n", err)
		return exitParamError
	}
	sc := cf.toSimConfig()
	if err := sc.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "jbbench: %v\n", err)
		return exitParamError
	}
	events, err := sim.Generate(sc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jbbench: scenario error: %v\n", err)
		return exitInputError
	}
	rep, err := receive.Replay(events, cf.frames, cf.playerConfig())
	if err != nil {
		fmt.Fprintf(os.Stderr, "jbbench: replay error: %v\n", err)
		return exitInputError
	}
	if err := metrics.Validate(rep); err != nil {
		fmt.Fprintf(os.Stderr, "jbbench: internal consistency failure: %v\n", err)
		return exitConsistency
	}
	if cf.json && fullJSON {
		if err := metrics.WriteJSON(os.Stdout, rep); err != nil {
			return exitInputError
		}
	} else {
		if err := metrics.WriteText(os.Stdout, rep); err != nil {
			return exitInputError
		}
	}
	return exitOK
}

func validateCommon(cf commonFlags) error {
	if cf.frames <= 0 {
		return fmt.Errorf("--frames must be positive, got %d", cf.frames)
	}
	if cf.windowCap <= 0 {
		return fmt.Errorf("--window must be positive, got %d", cf.windowCap)
	}
	if cf.lossBurst < 0 || cf.reorder < 0 {
		return fmt.Errorf("--loss-burst and --reorder must be non-negative")
	}
	if cf.duplicate < 0 || cf.duplicate > 1 || cf.corrupt < 0 || cf.corrupt > 1 ||
		cf.lossRate < 0 || cf.lossRate > 1 {
		return fmt.Errorf("rate flags must be within [0,1]")
	}
	if cf.duplicate+cf.corrupt+cf.lossRate > 1.5 {
		return fmt.Errorf("combined loss/duplicate/corrupt rates conflict: %.3f > 1.5",
			cf.duplicate+cf.corrupt+cf.lossRate)
	}
	return nil
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, "usage: jbbench <simulate|receive|report> [flags]")
	fmt.Fprintln(w, "  simulate  generate and replay an impaired scenario")
	fmt.Fprintln(w, "  receive   listen on loopback UDP and run the live path")
	fmt.Fprintln(w, "  report    regenerate the scenario and print a text summary")
	fmt.Fprintln(w, "flags: --frames --seed --loss-burst --loss-rate --reorder")
	fmt.Fprintln(w, "       --duplicate --corrupt --window --evict-oldest --json")
}
