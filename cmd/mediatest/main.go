// mediatest is the acceptance adapter: it generates all media samples
// in code, runs the ingest/processing/archive pipeline and prints
// verifiable metrics. Exit codes: 0 ok, 1 bad arguments, 2 input or
// recovery failure.
package main

import (
	"flag"
	"fmt"
	"os"
)

type params struct {
	outDir    string
	outRate   int
	channels  int
	targetRMS float64
	peakCeil  float64
}

func main() {
	var c string
	var p params
	flag.StringVar(&c, "case", "", "case to run: normal|corrupt|interrupt|all")
	flag.StringVar(&p.outDir, "out", "", "archive output directory (required)")
	flag.IntVar(&p.outRate, "rate", 48000, "output sample rate")
	flag.IntVar(&p.channels, "channels", 2, "output channel layout")
	flag.Float64Var(&p.targetRMS, "target-rms", -16.0, "target RMS loudness dBFS")
	flag.Float64Var(&p.peakCeil, "peak-ceil", -1.0, "peak ceiling dBFS")
	flag.Parse()
	if p.outDir == "" || p.outRate <= 0 || (p.channels != 1 && p.channels != 2) {
		fmt.Fprintln(os.Stderr, "usage: mediatest -case normal|corrupt|interrupt|all -out DIR [-rate 48000 -channels 2]")
		os.Exit(1)
	}
	var code int
	switch c {
	case "normal":
		code = caseNormal(p)
	case "corrupt":
		code = caseCorrupt(p)
	case "interrupt":
		code = caseInterrupt(p)
	case "all":
		if rc := caseNormal(p); rc != 0 {
			code = rc
		}
		if rc := caseInterrupt(p); rc != 0 && code == 0 {
			code = rc
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown case %q\n", c)
		os.Exit(1)
	}
	os.Exit(code)
}
