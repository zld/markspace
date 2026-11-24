package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediapipe/internal/model"
	"mediapipe/internal/pipeline"
	"mediapipe/internal/report"
	"mediapipe/internal/sample"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return 1
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "resume":
		return runResume(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		printUsage(os.Stderr)
		return 1
	}
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, `usage:
  mediapipe demo --out DIR [--inject truncation,corrupt,missing-index,bad-header]
                 [--target-bitrate BPS] [--segment-ms MS] [--checkpoint-packets N]
  mediapipe resume --out DIR
  mediapipe verify --out DIR`)
}

type commonFlags struct {
	out               string
	targetBitrate     int
	segmentMs         int64
	checkpointPackets int
	inject            string
}

func parseDemoFlags(args []string) (commonFlags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var f commonFlags
	fs.StringVar(&f.out, "out", "", "output directory")
	fs.IntVar(&f.targetBitrate, "target-bitrate", 64000, "target audio bitrate bps")
	fs.Int64Var(&f.segmentMs, "segment-ms", 2000, "segment duration in milliseconds")
	fs.IntVar(&f.checkpointPackets, "checkpoint-packets", 128, "packets per checkpoint block")
	fs.StringVar(&f.inject, "inject", "", "comma list: truncation,corrupt,missing-index,bad-header")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if f.out == "" {
		return f, errors.New("--out is required")
	}
	if f.targetBitrate <= 0 || f.segmentMs <= 0 || f.checkpointPackets <= 0 {
		return f, errors.New("bitrate, segment-ms and checkpoint-packets must be positive")
	}
	return f, nil
}

func runDemo(args []string) int {
	f, err := parseDemoFlags(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "argument error:", err)
		return 1
	}
	if err := os.MkdirAll(f.out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "cannot create out:", err)
		return 1
	}
	inputPath := filepath.Join(f.out, "input.mps")
	genOpts := sample.DefaultOptions(inputPath)
	if err := sample.Generate(genOpts); err != nil {
		fmt.Fprintln(os.Stderr, "generate sample stream:", err)
		return 1
	}

	injections := splitInject(f.inject)
	for _, name := range injections {
		switch name {
		case "truncation":
			if err := sample.TruncateTail(inputPath, 37); err != nil {
				fmt.Fprintln(os.Stderr, "inject truncation:", err)
				return 1
			}
		case "corrupt":
			if err := sample.CorruptRTPPayloads(inputPath, 3); err != nil {
				fmt.Fprintln(os.Stderr, "inject corruption:", err)
				return 1
			}
		case "bad-header":
			if err := sample.WriteBadHeader(inputPath); err != nil {
				fmt.Fprintln(os.Stderr, "inject bad header:", err)
				return 1
			}
		case "missing-index":
			// Applied after a first complete processing pass below.
		default:
			fmt.Fprintf(os.Stderr, "argument error: unknown injection %q\n", name)
			return 1
		}
	}

	opts := pipeline.DefaultOptions(f.out)
	opts.InputPath = inputPath
	opts.TargetBitrateBps = f.targetBitrate
	opts.SegmentMs = f.segmentMs
	opts.CheckpointPackets = f.checkpointPackets
	result, err := pipeline.Run(context.Background(), opts)
	if code := handleRunError(err); code != 0 {
		return code
	}
	if hasInject(injections, "missing-index") {
		if err := os.Remove(filepath.Join(f.out, "index.mp4")); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "remove index for missing-index injection:", err)
			return 1
		}
	}

	fmt.Println(report.PrintSummary(result.Manifest.Summary))
	fmt.Printf("manifest=%s\n", filepath.Join(f.out, "manifest.json"))
	fmt.Printf("index=%s\n", filepath.Join(f.out, "index.mp4"))
	if hasInject(injections, "missing-index") {
		fmt.Println("injected=missing-index: index.mp4 removed; run resume to rebuild it")
	}
	return 0
}

func runResume(args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	out := fs.String("out", "", "output directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "argument error: --out is required")
		return 1
	}
	opts := pipeline.DefaultOptions(*out)
	opts.Resume = true
	result, err := pipeline.Run(context.Background(), opts)
	if code := handleRunError(err); code != 0 {
		return code
	}
	fmt.Printf("resumed_from_packet=%d checkpoint_source=%s index_recovered=%t\n",
		result.ResumedFromPacket, result.CheckpointSource, result.IndexRecovered)
	fmt.Println(report.PrintSummary(result.Manifest.Summary))
	return 0
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	out := fs.String("out", "", "output directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "argument error: --out is required")
		return 1
	}
	v, err := report.VerifyOutput(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify error:", err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, "encode verification:", err)
		return 1
	}
	if !v.OK {
		return 1
	}
	fmt.Println("summary_consistent=true")
	return 0
}

func handleRunError(err error) int {
	if err == nil {
		return 0
	}
	var me *model.Error
	if errors.As(err, &me) && me.Class == model.ErrorHeader {
		fmt.Fprintf(os.Stderr, "fatal header error class=%s offset=%d: %v\n", me.Class, me.Offset, err)
		return 2
	}
	fmt.Fprintln(os.Stderr, "processing error:", err)
	return 1
}

func splitInject(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func hasInject(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
