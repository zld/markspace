// Command mediadist runs the local, deterministic media distribution
// demo. It never contacts network sources; all fixtures are generated
// in-process from the command-line parameters.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediadist/pipeline"
	"mediadist/timeline"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: mediadist demo [flags]")
		os.Exit(64)
	}

	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	loss := fs.Float64("loss", 0.02, "simulated edge packet loss rate in [0,1]")
	lateMs := fs.Int("late-ms", 180, "late-packet arrival offset in milliseconds")
	rates := fs.Int("rates", 3, "number of DASH representations")
	windowMs := fs.Int("replay-window-ms", 2000, "bounded replay window in milliseconds")
	corrupt := fs.Int("corrupt-timescale", 1, "set to 0 to inject a zero timescale")
	seed := fs.Int64("seed", 42, "deterministic fixture seed")
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(64)
	}

	cfg := pipeline.Config{
		Rates:          *rates,
		LossRate:       *loss,
		LateMs:         *lateMs,
		ReplayWindowMs: *windowMs,
		CorruptScale:   *corrupt,
		Seed:           *seed,
	}
	p, err := pipeline.New(cfg)
	if err != nil {
		emitError(err)
		os.Exit(2)
	}
	metrics, err := p.Run()
	if err != nil {
		emitError(err)
		switch timeline.CodeOf(err) {
		case "REPLAY_WINDOW_EXCEEDED":
			os.Exit(3)
		case "INVALID_TIMESCALE":
			os.Exit(2)
		default:
			os.Exit(1)
		}
	}
	fmt.Println(metrics.Format())
}

// emitError prints the stable machine-readable code first so terminal
// consumers can grep for error_code=... without parsing prose.
func emitError(err error) {
	code := timeline.CodeOf(err)
	if code == "" {
		code = "INTERNAL_ERROR"
	}
	fmt.Fprintf(os.Stderr, "error_code=%s detail=%q\n", code, err.Error())
}
