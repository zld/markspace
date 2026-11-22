// Command mediacore is the thin command-line acceptance adapter for the media
// kernel. It contains no media logic of its own; it parses flags, drives the
// deterministic simulator and maps the report status onto the documented exit
// codes (0 ok, 2 corrupt/invalid input, 3 metrics miss, 4 recovery mismatch).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"mediacore/internal/sim"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "verify" {
		fmt.Fprintln(os.Stderr, "usage: mediacore verify [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	seed := fs.Int64("seed", 42, "deterministic RNG seed")
	duration := fs.Duration("duration", 3600*time.Second, "simulated media duration (max 24h)")
	lossBurst := fs.Int("loss-burst", 3, "max consecutive audio packets lost (0..3)")
	driftPPM := fs.Int("drift-ppm", 120, "sender clock drift in ppm")
	chunkMS := fs.Int("chunk-ms", 20, "processing chunk size in milliseconds")
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}

	cfg := sim.Config{
		Seed:      *seed,
		DurationS: int64(*duration / time.Second),
		LossBurst: *lossBurst,
		DriftPPM:  *driftPPM,
		ChunkMS:   *chunkMS,
	}
	rep, err := sim.Run(cfg)
	if err != nil {
		// Invalid parameter or structurally rejected input.
		out, _ := json.Marshal(rep)
		fmt.Println(string(out))
		fmt.Fprintln(os.Stderr, "rejected:", err)
		os.Exit(2)
	}
	out, err := json.Marshal(rep)
	if err != nil {
		fmt.Fprintln(os.Stderr, "report encode failed:", err)
		os.Exit(2)
	}
	fmt.Println(string(out))
	switch rep.Status {
	case statusOK:
		os.Exit(0)
	case statusCheckpointBad:
		os.Exit(4)
	case statusMetricsFailed:
		os.Exit(3)
	default:
		os.Exit(2)
	}
}

const (
	statusOK            = "ok"
	statusCheckpointBad = "checkpoint_mismatch"
	statusMetricsFailed = "metrics_failed"
)
