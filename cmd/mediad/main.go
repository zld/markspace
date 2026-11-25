// mediad is the terminal-only acceptance entry point for the media processing
// kernel. Its `demo` subcommand synthesizes multichannel PCM/WAV and RTP
// fixtures locally, pushes them through the bounded pipeline, and prints the
// verification metrics. Exit codes: 0 complete, 2 input/index corruption,
// 3 internal write or cancellation failure.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediad/internal/fault"
	"mediad/internal/pipeline"
)

const (
	exitOK       = 0
	exitCorrupt  = 2
	exitInternal = 3
)

type flags struct {
	chunks            int
	framesPerChunk    int
	queue             int
	slowConsumerMs    int
	checkpointEvery   int
	corruptChunk      int
	truncateIndex     bool
	out               string
	channels          uint
	sampleRate        uint
	bits              uint
	packetsPerChunk   int
	maxReorder        uint
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitInternal)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(exitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(exitInternal)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: mediad demo [flags]

Generates local multichannel WAV/RTP fixtures and runs the bounded,
checkpointed remuxing pipeline.

flags:`+"\n")
	flag.PrintDefaults()
}

func parseFlags(args []string) (*flags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	f := &flags{corruptChunk: -1}
	fs.IntVar(&f.chunks, "chunks", 64, "number of media chunks to generate")
	fs.IntVar(&f.framesPerChunk, "frames-per-chunk", 1024, "PCM frames per chunk")
	fs.IntVar(&f.queue, "queue", 4, "bounded queue capacity in chunks")
	fs.IntVar(&f.slowConsumerMs, "slow-consumer-ms", 0, "per-chunk consumer delay")
	fs.IntVar(&f.checkpointEvery, "checkpoint-every", 8, "commit a snapshot every N chunks")
	fs.IntVar(&f.corruptChunk, "corrupt-chunk", -1, "truncate this chunk to force a recoverable fault (-1=off)")
	fs.BoolVar(&f.truncateIndex, "truncate-index", false, "damage the latest checkpoint to exercise recovery")
	fs.StringVar(&f.out, "out", "", "local output directory (default: temp dir)")
	fs.UintVar(&f.channels, "channels", 2, "input channels (1..8)")
	fs.UintVar(&f.sampleRate, "sample-rate", 48000, "PCM sample rate")
	fs.UintVar(&f.bits, "bits", 16, "PCM bits per sample (16/24/32)")
	fs.IntVar(&f.packetsPerChunk, "packets-per-chunk", 4, "RTP packets generated per chunk")
	fs.UintVar(&f.maxReorder, "max-reorder", 16, "RTP reorder window in packets")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return f, nil
}

func printMetrics(m *pipeline.Metrics, code int) {
	rate := func(n, d int) float64 {
		if d == 0 {
			return 0
		}
		return float64(n) / float64(d)
	}
	fmt.Printf("roundtrip_rate=%.3f\n", rate(m.RoundTripOK, m.Segments))
	fmt.Printf("keyframe_hit_rate=%.3f\n", rate(m.KeyframeHits, m.Segments))
	fmt.Printf("segment_hash_match_rate=%.3f\n", rate(m.HashMatches, m.Segments))
	fmt.Printf("peak_queue_chunks=%d\n", m.PeakQueueChunks)
	fmt.Printf("recovered_chunks=%d\n", m.RecoveredChunks)
	fmt.Printf("duplicate_packets=%d gap_packets=%d\n", m.DuplicatePackets, m.GapPackets)
	fmt.Printf("last_good_segment=%d resume_at_chunk=%d from_snapshot=%d\n",
		m.LastGoodSegment, m.ResumeAtChunk, m.FromSnapshot)
	fmt.Printf("exit_code=%d\n", code)
}

func reportError(err error) int {
	code := fault.ExitCode(err)
	if fe, ok := fault.As(err); ok {
		fmt.Fprintf(os.Stderr, "error category=%s offset=%d track=%d recoverable=%t: %v\n",
			fe.Category, fe.Offset, fe.Track, fe.Recoverable, fe)
	} else {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	fmt.Printf("exit_code=%d\n", code)
	return code
}
