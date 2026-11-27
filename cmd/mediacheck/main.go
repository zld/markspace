// Command mediacheck is the terminal adapter over the reusable media
// analysis kernel. It only parses flags, drives the pipeline and prints
// machine-readable JSON; all analysis lives in the internal packages.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"mediacheck/internal/codec"
	mediaerr "mediacheck/internal/errors"
	"mediacheck/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return mediaerr.ExitInputOrIndex
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "analyze":
		return runAnalyze(args[1:])
	case "-h", "--help", "help":
		usage()
		return mediaerr.ExitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return mediaerr.ExitInputOrIndex
	}
}

// demoFlags holds the parameters shared by the demo command.
type demoFlags struct {
	codec         string
	chunkBytes    int
	queue         int
	segmentBytes  int64
	faults        string
	interrupt     string
	resume        bool
	json          bool
	outDir        string
	runID         string
}

func parseDemoFlags(fs *flag.FlagSet) *demoFlags {
	d := &demoFlags{}
	fs.StringVar(&d.codec, "codec", "all", "codec set: h264|h265|av1|all")
	fs.IntVar(&d.chunkBytes, "chunk-bytes", 4096, "read chunk size in bytes")
	fs.IntVar(&d.queue, "queue", 2, "bounded queue capacity")
	fs.Int64Var(&d.segmentBytes, "segment-bytes", 0, "hash window bytes (default=chunk)")
	fs.StringVar(&d.faults, "faults", "none", "comma list: none|truncated|invalid-length")
	fs.StringVar(&d.interrupt, "interrupt-after-stage", "",
		"stop after stage: scan|parse|index (for resume testing)")
	fs.BoolVar(&d.resume, "resume", false, "resume a previously interrupted run")
	fs.BoolVar(&d.json, "json", false, "emit machine-readable JSON")
	fs.StringVar(&d.outDir, "out", "", "txn/output directory (default temp dir)")
	fs.StringVar(&d.runID, "run-id", "demo-run", "stable run identifier")
	return d
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	d := parseDemoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return mediaerr.ExitInputOrIndex
	}

	outDir := d.outDir
	if outDir == "" {
		var err error
		outDir, err = os.MkdirTemp("", "mediacheck-demo-")
		if err != nil {
			fmt.Fprintf(os.Stderr, "temp dir: %v\n", err)
			return mediaerr.ExitGeneric
		}
	}

	// A non-resume invocation starts clean so an old run cannot leak state.
	if !d.resume {
		if err := os.RemoveAll(outDir); err != nil {
			fmt.Fprintf(os.Stderr, "clean out dir: %v\n", err)
			return mediaerr.ExitGeneric
		}
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
		return mediaerr.ExitGeneric
	}

	specs, err := buildDemoSpecs(d)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo setup: %v\n", err)
		return mediaerr.ExitInputOrIndex
	}

	cfg := pipeline.Config{
		ChunkBytes: d.chunkBytes, QueueLen: d.queue,
		SegmentBytes: d.segmentBytes,
		ToleranceTicks: 3,
		InterruptAfterStage: d.interrupt,
	}

	// Cancel on Ctrl-C so every goroutine exits promptly.
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runner, err := pipeline.NewRunner(outDir, cfg)
	if err != nil {
		return failWith(err)
	}

	inputs := make([]pipeline.TrackInput, 0, len(specs))
	for _, s := range specs {
		gen, err := pipeline.Generate(s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "generate: %v\n", err)
			return mediaerr.ExitInputOrIndex
		}
		inputs = append(inputs, gen.TrackInput())
	}

	report, err := runner.Run(ctx, d.runID, inputs)
	if err != nil && err != pipeline.ErrInterrupted {
		return failWith(err)
	}
	interrupted := err == pipeline.ErrInterrupted

	if d.json {
		return printJSON(report, outDir, interrupted)
	}
	printHuman(report, outDir, interrupted)
	return mediaerr.ExitOK
}

// buildDemoSpecs maps flags into deterministic generator specs, applying the
// requested faults to suitable codecs (invalid-length needs prefix framing).
func buildDemoSpecs(d *demoFlags) ([]pipeline.DemoSpec, error) {
	faults := splitCSV(d.faults)
	want := map[codec.Codec]bool{}
	switch d.codec {
	case "all":
		want[codec.H264] = true
		want[codec.H265] = true
		want[codec.AV1] = true
	case "h264", "h265", "av1":
		want[codec.Codec(d.codec)] = true
	default:
		return nil, fmt.Errorf("unknown codec %q", d.codec)
	}

	var specs []pipeline.DemoSpec
	for _, c := range []codec.Codec{codec.H264, codec.H265, codec.AV1} {
		if !want[c] {
			continue
		}
		framing := codec.AnnexB
		if c == codec.AV1 {
			framing = codec.LengthPrefixed
		}
		fault := pickFault(faults, framing)
		specs = append(specs, pipeline.DemoSpec{
			Track: "track-" + string(c), Codec: c, Framing: framing,
			Frames: 30, KeyInterval: 5, Fault: fault,
		})
	}
	return specs, nil
}

// pickFault chooses a fault this framing can express. A length-prefixed
// stream that requests both faults gets the combined injection; Annex B
// streams express truncation, and invalid-length only applies to prefixes.
func pickFault(faults []string, framing codec.Framing) pipeline.Fault {
	hasTrunc, hasBadLen := false, false
	for _, f := range faults {
		switch pipeline.Fault(f) {
		case pipeline.FaultTruncated:
			hasTrunc = true
		case pipeline.FaultInvalidLength:
			hasBadLen = true
		}
	}
	if framing == codec.LengthPrefixed && hasTrunc && hasBadLen {
		return pipeline.FaultBoth
	}
	if hasBadLen && framing == codec.LengthPrefixed {
		return pipeline.FaultInvalidLength
	}
	if hasTrunc {
		return pipeline.FaultTruncated
	}
	return pipeline.FaultNone
}

func runAnalyze(_ []string) int {
	// The analyze command is the same kernel driven against user files; the
	// acceptance route uses the self-contained demo, so this stays minimal.
	fmt.Fprintln(os.Stderr, "analyze: use 'demo' for the self-contained flow")
	return mediaerr.ExitOK
}

// envelope is the top-level machine-readable output contract.
type envelope struct {
	OK                       bool                      `json:"ok"`
	Interrupted              bool                      `json:"interrupted,omitempty"`
	Recovered                bool                      `json:"recovered"`
	RoundtripConsistency     float64                   `json:"roundtrip_consistency"`
	KeyframeIndexHitRate     float64                   `json:"keyframe_index_hit_rate"`
	SegmentSHA256Count       int                       `json:"segment_sha256_count"`
	DamageRanges             int                       `json:"damage_ranges"`
	PeakBufferBytes          int64                     `json:"peak_buffer_bytes"`
	BufferLimitBytes         int64                     `json:"buffer_limit_bytes"`
	ReplayedCommittedStages  []string                  `json:"replayed_committed_stages,omitempty"`
	OutDir                   string                    `json:"out_dir"`
	Report                   *pipeline.Report          `json:"report"`
}

func printJSON(rep *pipeline.Report, outDir string, interrupted bool) int {
	env := envelope{
		OK:               rep != nil,
		Interrupted:      interrupted,
		OutDir:           outDir,
		Report:           rep,
	}
	if rep != nil {
		env.Recovered = rep.Recovered
		env.RoundtripConsistency = rep.Metrics["roundtrip_consistency"]
		env.KeyframeIndexHitRate = rep.Metrics["keyframe_index_hit_rate"]
		env.SegmentSHA256Count = int(rep.Metrics["segment_sha256_count"])
		env.DamageRanges = int(rep.Metrics["damage_range_count"])
		env.PeakBufferBytes = rep.PeakBuffer
		env.BufferLimitBytes = rep.BufferLimit
		env.ReplayedCommittedStages = rep.Recommitted
	}
	raw, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		return mediaerr.ExitGeneric
	}
	fmt.Println(string(raw))
	return mediaerr.ExitOK
}

func printHuman(rep *pipeline.Report, outDir string, interrupted bool) {
	fmt.Printf("mediacheck %s\n", outDir)
	if rep == nil {
		fmt.Println("interrupted before a report was produced")
		return
	}
	fmt.Printf("recovered=%v tracks=%d damage=%d hit_rate=%.3f segments=%d peak_buf=%d\n",
		rep.Recovered, len(rep.Tracks),
		int(rep.Metrics["damage_range_count"]),
		rep.Metrics["keyframe_index_hit_rate"],
		int(rep.Metrics["segment_sha256_count"]), rep.PeakBuffer)
	if interrupted {
		fmt.Println("interrupted after stage:", "scan/parse/index")
	}
}

// failWith maps a kernel error to the documented terminal exit code.
func failWith(err error) int {
	code := mediaerr.ExitCode(err)
	if me, ok := mediaerr.AsMedia(err); ok {
		fmt.Fprintf(os.Stderr, "%s\n", me.Error())
	} else {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}
	return code
}

func splitCSV(s string) []string {
	if s == "" || s == "none" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			tok := s[start:i]
			if tok != "" {
				out = append(out, tok)
			}
			start = i + 1
		}
	}
	return out
}

func usage() {
	fmt.Fprintln(os.Stderr, `mediacheck - local elementary stream analysis kernel

usage:
  mediacheck demo --codec all --chunk-bytes 4096 --queue 2 \
      --faults truncated,invalid-length \
      --interrupt-after-stage scan --resume --json

exit codes:
  0 completed and summary verified
  2 input or index error
  3 commit or recovery verification failure`)
}

