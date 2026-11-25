package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"mediapack/model"
	"mediapack/pipeline"
)

func main() {
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

type cliErrorReport struct {
	Status  string           `json:"status"`
	OK      bool             `json:"ok"`
	Code    int              `json:"exit_code"`
	Error   *model.Error     `json:"error,omitempty"`
	Message string           `json:"message,omitempty"`
	Result  *pipeline.Result `json:"result,omitempty"`
}

func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) < 2 || argv[1] != "demo" {
		fmt.Fprintln(stderr, "usage: mediapack demo [--seed N] [--bitrates N] [--queue-capacity N] [--report json|text] ...")
		return 2
	}

	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 17, "deterministic sample seed")
	bitrates := fs.Int("bitrates", 2, "number of generated bitrate representations (2-4)")
	queueCapacity := fs.Int("queue-capacity", 8, "fixed bounded queue capacity")
	output := fs.String("output", "", "output directory (scenario-specific default under ./output)")
	corrupt := fs.String("corrupt", "", "corruption mode: truncate|invalid-length|missing-keyframe|crc")
	lossSpec := fs.String("loss", "", "comma-separated RTP sequence numbers to lose, e.g. 0,3,4")
	reorder := fs.Int("reorder", 0, "deterministic RTP reorder depth/window")
	resume := fs.Bool("resume", false, "resume and verify an existing manifest")
	reportFormat := fs.String("report", "json", "report format: json|text")
	if err := fs.Parse(argv[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return 2
	}

	losses, err := parseLosses(*lossSpec)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	outputDir := *output
	if outputDir == "" {
		outputDir = defaultOutputDir(*corrupt, *lossSpec, *reorder)
	}

	cfg := pipeline.Config{
		Seed: *seed, BitrateCount: *bitrates, QueueCapacity: *queueCapacity,
		OutputDir: outputDir, Corrupt: *corrupt, Losses: losses,
		Reorder: *reorder, Resume: *resume, ReportFormat: *reportFormat,
	}
	result, err := pipeline.Run(cfg)
	if err != nil {
		code := pipeline.ExitCodeFor(err)
		writeCLIError(stdout, cfg.ReportFormat, code, err, result)
		return code
	}

	if cfg.ReportFormat == "text" {
		writeText(stdout, result)
		return 0
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintln(stderr, "encode report:", err)
		return 1
	}
	return 0
}

func parseLosses(spec string) ([]int, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	parts := strings.Split(spec, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid --loss value %q", part)
		}
		out = append(out, n)
	}
	return out, nil
}

func defaultOutputDir(corrupt, lossSpec string, reorder int) string {
	if corrupt != "" {
		return "output/corrupt-" + corrupt
	}
	if strings.TrimSpace(lossSpec) != "" || reorder > 0 {
		return "output/rtp-recovery"
	}
	return "output/demo"
}

func writeCLIError(w io.Writer, format string, code int, err error, result *pipeline.Result) {
	me, _ := model.AsError(err)
	envelope := cliErrorReport{
		Status: "error", OK: false, Code: code, Error: me, Message: err.Error(), Result: result,
	}
	if format == "text" {
		fmt.Fprintf(w, "ERROR(%d): %v\n", code, err)
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(envelope)
}

func writeText(w io.Writer, r *pipeline.Result) {
	fmt.Fprintf(w, "status=%s manifest=%s sha256=%s\n", r.Status, r.ManifestPath, r.ManifestSHA256)
	fmt.Fprintf(w, "av_sync_ms=%.3f p95_latency_ms=%.3f peak_queue=%d/%d segments=%d unrecovered=%d\n",
		r.Metrics.AVSyncErrorMS, r.Metrics.P95LatencyMS,
		r.Metrics.PeakQueueOccupancy, r.Metrics.QueueCapacity,
		r.Metrics.SegmentCount, r.Metrics.UnrecoveredPackets)
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "warning category=%s offset=%d message=%s\n", warning.Category, warning.Offset, warning.Message)
	}
}
