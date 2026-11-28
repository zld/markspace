// Command medcheck is the terminal acceptance adapter for the media processing
// kernel. It self-generates every sample, runs the deterministic pipeline and
// exits non-zero if any acceptance assertion fails.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"medcheck/internal/pipeline"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "medcheck: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("medcheck", flag.ContinueOnError)
	caseName := fs.String("case", "all", "which sample case to run (all)")
	lateWindow := fs.Int64("late-window", 3, "allowed lateness in microseconds")
	emitHashes := fs.Bool("emit-hashes", false, "print shard and segment SHA-256 hashes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caseName != "all" {
		return fmt.Errorf("unsupported --case %q (only \"all\" is generated)", *caseName)
	}
	if *lateWindow < 0 {
		return fmt.Errorf("--late-window must be non-negative")
	}

	res, err := pipeline.Run(pipeline.Config{
		LateWindowMicros:  *lateWindow,
		ShardWindowMicros: 100,
	})
	if err != nil {
		return err
	}

	printMetrics(res, *emitHashes)
	return assert(res)
}

func printMetrics(res *pipeline.Result, emitHashes bool) {
	fmt.Printf("parse_roundtrip=%.3f\n", res.ParseRoundTrip)
	fmt.Printf("keyframe_hit=%.3f\n", res.KeyframeHit)
	fmt.Printf("bad_segments=%d\n", res.BadSegments)
	fmt.Printf("offset_trace=%.3f\n", res.OffsetTrace)
	fmt.Printf("replay_equal=%t\n", res.ReplayEqual)
	fmt.Printf("segment_sha256_count=%d\n", res.SegmentSHA256Count)
	fmt.Printf("recompute_forward=%d recompute_reverse=%d\n",
		res.RecomputeForward, res.RecomputeReverse)
	if emitHashes {
		fmt.Printf("summary_hash_forward=%s\n", res.SummaryHashForward)
		fmt.Printf("summary_hash_reverse=%s\n", res.SummaryHashReverse)
		shards := append([]string{}, res.ShardHashes...)
		sort.Strings(shards)
		for _, h := range shards {
			fmt.Printf("shard_sha256=%s\n", h)
		}
		segs := append([]string{}, res.SegmentHashes...)
		sort.Strings(segs)
		for _, h := range segs {
			fmt.Printf("segment_sha256=%s\n", h)
		}
	}
	for _, n := range res.Notes {
		fmt.Printf("note=%s\n", n)
	}
}

// assert encodes the fixed acceptance conditions from the specification.
func assert(res *pipeline.Result) error {
	var failures []string
	check := func(name string, ok bool) {
		if !ok {
			failures = append(failures, name)
		}
	}
	check("parse_roundtrip==1.000", res.ParseRoundTrip == 1.0)
	check("keyframe_hit==1.000", res.KeyframeHit == 1.0)
	check("bad_segments>=2", res.BadSegments >= 2)
	check("offset_trace==1.000", res.OffsetTrace == 1.0)
	check("replay_equal==true", res.ReplayEqual)
	check("segment_sha256_count>=1", res.SegmentSHA256Count >= 1)
	check("summary_hash_forward==reverse",
		res.SummaryHashForward != "" && res.SummaryHashForward == res.SummaryHashReverse)
	if len(failures) > 0 {
		return fmt.Errorf("acceptance assertions failed: %v", failures)
	}
	fmt.Println("all_assertions_passed=true")
	return nil
}
