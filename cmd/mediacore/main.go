// Command mediacore is the terminal entry point of the media kernel.
// All functionality is driven by run parameters; results are printed to
// the terminal as key=value lines.
//
// Exit codes: 0 = processing complete (including recovered faults),
// 2 = corrupt input quarantined, 1 = internal error.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediacore/media"
)

type demoFlags struct {
	seed         int64
	loss         float64
	burst        int
	channels     int
	rate         int
	targetRate   int
	resumeCheck  bool
	corruptIndex bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: mediacore demo [--seed N] [--loss F] [--burst N] [--channels N] [--rate HZ] [--target-rate HZ] [--resume-check] [--corrupt-index]")
		return 1
	}
	var f demoFlags
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.Int64Var(&f.seed, "seed", 42, "deterministic sample seed")
	fs.Float64Var(&f.loss, "loss", 0.08, "simulated packet loss rate")
	fs.IntVar(&f.burst, "burst", 5, "loss burst length in packets")
	fs.IntVar(&f.channels, "channels", 6, "input channels: 1, 2, or 6")
	fs.IntVar(&f.rate, "rate", 44100, "input sample rate: 44100 or 48000")
	fs.IntVar(&f.targetRate, "target-rate", 48000, "output sample rate")
	fs.BoolVar(&f.resumeCheck, "resume-check", false, "verify crash/resume digest consistency")
	fs.BoolVar(&f.corruptIndex, "corrupt-index", false, "inject a corrupt keyframe index")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if err := runDemo(f); err != nil {
		if media.ClassOf(media.CodeOf(err)) == media.ClassInput {
			fmt.Printf("error_code=%s\n", media.CodeOf(err))
			return 2
		}
		fmt.Fprintf(os.Stderr, "internal error: %v\n", err)
		return 1
	}
	return 0
}
