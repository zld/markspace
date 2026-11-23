// Command archivist runs the local media-preservation demo end to end.
// It generates an MP4/MOV fixture, parses and repairs it, aligns multi-bitrate
// keyframe boundaries, cuts and hashes segments, and publishes a manifest.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"archivist/internal/container"
	"archivist/internal/model"
	"archivist/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	bitrateFlag := fs.String("bitrates", "800000,1600000", "comma-separated bitrates in bps")
	segmentMS := fs.Uint("segment-ms", 2000, "target segment duration in milliseconds")
	corruption := fs.String("corrupt", "", "corruption mode to inject")
	retry := fs.Int("retry", 0, "transient-failure retry count")
	outDir := fs.String("out", ".archivist-out", "directory for manifest and state")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	bitrates, err := parseBitrates(*bitrateFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --bitrates: %v\n", err)
		return 1
	}
	if *corruption != "" && !container.Corruptable(*corruption) {
		fmt.Fprintf(os.Stderr, "unknown --corrupt %q\n", *corruption)
		return 1
	}

	cfg := pipeline.Config{
		Seed:       *seed,
		Bitrates:   bitrates,
		SegmentMS:  uint32(*segmentMS),
		Corruption: *corruption,
		Retries:    *retry,
		OutputDir:  *outDir,
	}
	runner := pipeline.New(cfg)
	metrics, err := runner.Run()
	printMetrics(metrics)

	if err == nil {
		return 0
	}
	// Exit 2: input corrupted and explicitly rejected (recoverable inputs
	// still return 0 after rebuilding a verifiable index).
	var ae *model.ArchiveError
	if errors.As(err, &ae) && metrics.ExitCode == 2 {
		fmt.Fprintf(os.Stderr, "rejected: %s\n", err.Error())
		return 2
	}
	// Exit 1: permanent dependency failure or internal error.
	fmt.Fprintf(os.Stderr, "error: %s\n", err.Error())
	if metrics.ExitCode == 0 {
		return 1
	}
	return metrics.ExitCode
}

func parseBitrates(s string) ([]uint32, error) {
	parts := strings.Split(s, ",")
	if len(parts) < 2 {
		return nil, errors.New("at least two bitrates are required")
	}
	out := make([]uint32, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseUint(strings.TrimSpace(p), 10, 32)
		if err != nil || v == 0 {
			return nil, fmt.Errorf("bad bitrate %q", p)
		}
		out = append(out, uint32(v))
	}
	return out, nil
}

func printMetrics(m *pipeline.Metrics) {
	fmt.Printf("roundtrip_rate=%.3f\n", m.RoundtripRate)
	fmt.Printf("keyframe_hit_rate=%.3f\n", m.KeyframeHitRate)
	fmt.Printf("segments=%d\n", m.Segments)
	fmt.Printf("aligned_boundaries=%s\n", m.AlignedBoundaries)
	fmt.Printf("segment_hash_match=%s\n", m.SegmentHashMatch)
	fmt.Printf("recovered_indexes=%d\n", m.RecoveredIndexes)
	fmt.Printf("blocked_by=%s\n", m.BlockedBy)
	fmt.Printf("manifest_written=%t\n", m.ManifestWritten)
	fmt.Printf("rerun_digest_equal=%t\n", m.RerunDigestEqual)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: archivist demo [flags]")
	fmt.Fprintln(os.Stderr, "flags:")
	fmt.Fprintln(os.Stderr, "  --seed int          deterministic fixture seed")
	fmt.Fprintln(os.Stderr, "  --bitrates a,b      comma-separated bitrates (>=2)")
	fmt.Fprintln(os.Stderr, "  --segment-ms uint   target segment duration")
	fmt.Fprintln(os.Stderr, "  --corrupt string    none|truncated-box|corrupt-sample-table|")
	fmt.Fprintln(os.Stderr, "                      missing-keyframe-index|audio-start-offset|keyframe-drift")
	fmt.Fprintln(os.Stderr, "  --retry int         transient retry count")
	fmt.Fprintln(os.Stderr, "  --out string        output directory")
}
