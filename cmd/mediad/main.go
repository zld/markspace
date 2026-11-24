// Command mediad is the terminal acceptance entry point for the reusable
// media processing kernel.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"mediad/internal/demo"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: mediad demo [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 23, "deterministic sample seed")
	driftPPM := fs.Float64("drift-ppm", 120, "remote clock drift in parts per million")
	wrapAt := fs.Uint64("wrap-at", 4294967000, "initial near-wrap 32-bit RTP timestamp")
	vfr := fs.Bool("vfr", true, "generate explicit variable-frame-rate timestamps")
	chunk := fs.String("chunk", "2s", "chunk duration (Go duration)")
	targetRate := fs.Int("target-rate", 48000, "target audio sample rate")
	layout := fs.String("layout", "stereo", "target channel layout: mono, stereo, 5.1")
	loudness := fs.Float64("loudness", -16, "integrated loudness target in LUFS")
	peakCeiling := fs.Float64("peak-ceiling", -1.0, "peak ceiling in dBFS")
	out := fs.String("out", "/tmp/mediad-demo", "output directory")
	corruptIndex := fs.Bool("corrupt-index", false, "fault: truncate checkpoint index before recovery")
	truncateChunk := fs.Bool("truncate-chunk", false, "fault: partially write a committed chunk")
	lateOverlap := fs.Bool("late-overlap", false, "fault: submit a late segment overlapping committed range")

	if err := fs.Parse(os.Args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(2)
	}
	chunkDuration, err := time.ParseDuration(*chunk)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --chunk %q: %v\n", *chunk, err)
		os.Exit(2)
	}
	if *wrapAt > uint64(^uint32(0)) {
		fmt.Fprintln(os.Stderr, "--wrap-at must fit uint32")
		os.Exit(2)
	}

	opt := demo.Options{
		Out:            *out,
		Seed:           *seed,
		DriftPPM:       *driftPPM,
		WrapAt:         uint32(*wrapAt),
		VFR:            *vfr,
		Chunk:          chunkDuration,
		ChunkString:    chunkDuration.String(),
		TargetRate:     *targetRate,
		LayoutName:     *layout,
		LoudnessTarget: *loudness,
		PeakCeilingDB:  *peakCeiling,
		CorruptIndex:   *corruptIndex,
		TruncateChunk:  *truncateChunk,
		LateOverlap:    *lateOverlap,
	}
	report, runErr := demo.Run(opt)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)

	if report.IndexRebuilt {
		fmt.Fprintln(os.Stderr, "索引重建完成 / index rebuilt: invalid checkpoint was reconstructed and incomplete chunks were redone")
	}
	if len(report.TruncatedChunksRedone) > 0 {
		fmt.Fprintf(os.Stderr, "截断块重做 / truncated chunks redone: %v\n", report.TruncatedChunksRedone)
	}
	if report.OverlapRejected {
		fmt.Fprintln(os.Stderr, "重叠片段拒绝 / late overlap rejected: overlapping input was not committed and the prior checkpoint was preserved")
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "mediad demo failed: %v\n", runErr)
		os.Exit(1)
	}
}
