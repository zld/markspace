// Command streamcheck runs the RTP/RTCP stream analysis over generated
// scenario samples and prints deterministic reports.
package main

import (
	"flag"
	"fmt"
	"os"

	"streamcheck/pipeline"
)

func main() {
	seed := flag.Int64("seed", 42, "scenario RNG seed")
	scenario := flag.String("scenario", "normal", "scenario name or \"all\"")
	watermarkMs := flag.Int64("watermark-ms", 250, "watermark lag in milliseconds")
	flag.Parse()

	if *watermarkMs < 0 {
		fmt.Fprintln(os.Stderr, "watermark-ms must be >= 0")
		os.Exit(2)
	}
	var scenarios []string
	switch {
	case *scenario == "all":
		scenarios = pipeline.Scenarios
	case pipeline.IsScenario(*scenario):
		scenarios = []string{*scenario}
	default:
		fmt.Fprintf(os.Stderr, "unknown scenario %q (want one of %v or \"all\")\n",
			*scenario, pipeline.Scenarios)
		os.Exit(2)
	}

	failed := false
	for _, sc := range scenarios {
		rep, err := pipeline.Run(pipeline.Options{
			Seed: *seed, Scenario: sc, WatermarkMs: *watermarkMs,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "scenario=%s error=%v\n", sc, err)
			failed = true
			continue
		}
		fmt.Print(rep.Format())
	}
	if failed {
		os.Exit(1)
	}
}
