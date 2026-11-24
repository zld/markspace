// Command archive runs the media archive kernel demo. It generates normal,
// boundary and corrupt fixtures, composes a multi-track timeline, recovers
// representation layers over a lossy RTP feed, fans them into a shared clock,
// renders PNG evidence and writes a deterministic manifest.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"archive/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: archive demo [flags]

  --generate            generate sample fixtures (always on for demo)
  --out DIR             output directory for assets, frames, manifest
  --loss-burst N        consecutive packets dropped to simulate burst loss
  --bandwidth-kbps LIST comma-separated bandwidth samples in kbps
`)
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	generate := fs.Bool("generate", false, "generate sample fixtures")
	out := fs.String("out", "", "output directory")
	lossBurst := fs.Int("loss-burst", 3, "consecutive packets dropped")
	bw := fs.String("bandwidth-kbps", "900,420,1100", "comma-separated bandwidth samples")
	frameLimit := fs.Int("frame-limit", 3, "frames rendered per track")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "error: --out is required")
		return 2
	}
	if *lossBurst < 0 {
		fmt.Fprintln(os.Stderr, "error: --loss-burst must be non-negative")
		return 2
	}
	bandwidth, err := parseList(*bw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid --bandwidth-kbps: %v\n", err)
		return 2
	}
	opts := pipeline.DefaultOptions()
	opts.OutDir = *out
	opts.Generate = *generate
	opts.LossBurst = *lossBurst
	opts.Bandwidth = bandwidth
	opts.FrameLimit = *frameLimit

	res, err := pipeline.Run(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archive demo failed: %v\n", err)
		return pipeline.ClassifyExit(err)
	}
	if err := pipeline.PrintSummary(os.Stdout, res.Summary); err != nil {
		fmt.Fprintf(os.Stderr, "archive demo failed to print summary: %v\n", err)
		return 2
	}
	if res.ExitCode != 0 {
		fmt.Fprintf(os.Stderr, "archive demo: recovery incomplete: %v\n", res.Err)
	}
	return res.ExitCode
}

func parseList(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("empty list")
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", p)
		}
		if v <= 0 {
			return nil, fmt.Errorf("value %d must be positive", v)
		}
		out = append(out, v)
	}
	return out, nil
}
