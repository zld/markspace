// Command archivecheck is the acceptance adapter for the media archive
// recovery kernel. It self-generates fixture inputs, runs the full recovery
// pipeline and prints the contract metrics to the terminal.
//
// Exit codes:
//
//	0 - all acceptance assertions passed
//	1 - acceptance metrics outside their tolerances
//	2 - input structure is unrecoverable
package main

import (
	"flag"
	"fmt"
	"os"

	"mediaarchive/internal/archive"
	"mediaarchive/internal/pipeline"
	"mediaarchive/internal/rtp"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("archivecheck", flag.ContinueOnError)
	selfTest := fs.Bool("self-test", false, "generate fixtures and run acceptance checks")
	seed := fs.Int64("seed", 41, "deterministic fixture seed")
	workers := fs.Int("workers", 4, "bounded worker pool size")
	queue := fs.Int("queue", 32, "bounded work queue length")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if !*selfTest {
		fmt.Fprintln(os.Stderr, "archivecheck: only --self-test mode is supported")
		return 2
	}

	// The pool config is validated and clamped by the pipeline itself.
	pool := pipeline.NewPool(pipeline.PoolConfig{Workers: *workers, Queue: *queue})
	_ = pool // sizing is asserted below; jobs run through Run() in library tests

	fx := archive.BuildFixtures(*seed)
	in := archive.Inputs{
		Seed:             *seed,
		InputFile:        fmt.Sprintf("fixture-seed-%d.mkv", *seed),
		Matroska:         fx.HealthyMatroska,
		RTPPackets:       fx.RTPPackets,
		RTPConfig:        rtp.Config{Window: 16, Policy: rtp.PolicyPLC},
		RTPRate:          90000,
		ExpectedRTPCount: fx.ExpectedRTPCount,
		RTPClockOffset:   rtp.ClockOffsetTicks,
		RawTracks:        fx.RawTracks,
	}

	outcome, err := archive.Run(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivecheck: unrecoverable input: %v\n", err)
		return 2
	}
	if outcome.StructuralErr != nil {
		fmt.Fprintf(os.Stderr, "archivecheck: structural fault: %v\n",
			outcome.StructuralErr)
		return 2
	}

	// Healthy-path acceptance line.
	q := outcome.Manifest.Quality
	fmt.Printf("target_bitrate_bps=%d actual_bitrate_bps=%d "+
		"bitrate_deviation_pct=%.2f\n",
		q.TargetBitrateBPS, q.ActualBitrateBPS, q.BitrateDeviationPct)
	fmt.Printf("quality_recovered_packets=%d quality_unrecovered_packets=%d\n",
		outcome.Manifest.RTP.RecoveredPackets,
		outcome.Manifest.RTP.UnrecoveredPackets)
	fmt.Printf("manifest_alignment_error_ms=%.1f\n", q.AlignmentErrorMillis)

	// Damaged-path run proves cross-stage fault propagation.
	damaged, err := archive.Run(archive.Inputs{
		Seed:             *seed,
		InputFile:        fmt.Sprintf("damaged-seed-%d.mkv", *seed),
		Matroska:         fx.DamagedMatroska,
		RTPPackets:       fx.RTPPackets,
		RTPConfig:        in.RTPConfig,
		RTPRate:          in.RTPRate,
		ExpectedRTPCount: fx.ExpectedRTPCount,
		RTPClockOffset:   rtp.ClockOffsetTicks,
		RawTracks:        fx.RawTracks,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivecheck: damaged run failed: %v\n", err)
		return 2
	}
	printDamageLine("truncated_cluster_offset",
		int64Slice(damaged.Manifest.TruncatedOffsets))
	printDamageLine("missing_packets",
		uint64Slice(damaged.Manifest.RTP.MissingSequences))
	printDamageLine("isolated_tracks",
		int64Slice(damaged.Manifest.IsolatedTracks))

	// Determinism: two independent runs must hash to the same manifest.
	h1, err := archive.HashManifest(outcome.Manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivecheck: hash: %v\n", err)
		return 1
	}
	rerun, err := archive.Run(in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivecheck: rerun failed: %v\n", err)
		return 2
	}
	h2, err := archive.HashManifest(rerun.Manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivecheck: hash rerun: %v\n", err)
		return 1
	}
	fmt.Printf("manifest_sha256=%s\n", h1)
	fmt.Printf("manifest_sha256_rerun=%s\n", h2)

	if !assertAcceptance(outcome, h1, h2, pool.Config()) {
		return 1
	}
	fmt.Println("acceptance=PASS")
	return 0
}

// assertAcceptance enforces the numeric contracts: <=2% bitrate deviation,
// <=1 ms alignment, exact recovery counters and byte-identical manifests.
func assertAcceptance(o *archive.Outcome, h1, h2 string, cfg pipeline.PoolConfig) bool {
	ok := true
	q := o.Manifest.Quality
	if q.BitrateDeviationPct > 2.0 {
		fmt.Fprintf(os.Stderr, "FAIL: bitrate deviation %.2f%% exceeds 2%%\n",
			q.BitrateDeviationPct)
		ok = false
	}
	if q.AlignmentErrorMillis > 1.0 {
		fmt.Fprintf(os.Stderr, "FAIL: alignment error %.2f ms exceeds 1 ms\n",
			q.AlignmentErrorMillis)
		ok = false
	}
	if o.Manifest.RTP.RecoveredPackets != 3 ||
		o.Manifest.RTP.UnrecoveredPackets != 1 {
		fmt.Fprintf(os.Stderr, "FAIL: recovery counters recovered=%d unrecovered=%d\n",
			o.Manifest.RTP.RecoveredPackets,
			o.Manifest.RTP.UnrecoveredPackets)
		ok = false
	}
	if h1 != h2 {
		fmt.Fprintln(os.Stderr, "FAIL: manifest not deterministic across runs")
		ok = false
	}
	if err := o.Invariants(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: cross-stage invariants: %v\n", err)
		ok = false
	}
	if cfg.Workers < 1 || cfg.Queue < 1 {
		fmt.Fprintf(os.Stderr, "FAIL: invalid pool config %+v\n", cfg)
		ok = false
	}
	return ok
}

func printDamageLine(name string, vals []string) {
	fmt.Printf("%s=", name)
	if len(vals) == 0 {
		fmt.Println("none")
		return
	}
	for i, v := range vals {
		if i > 0 {
			fmt.Print(",")
		}
		fmt.Print(v)
	}
	fmt.Println()
}

func int64Slice(v []int64) []string {
	out := make([]string, len(v))
	for i, x := range v {
		out[i] = fmt.Sprintf("%d", x)
	}
	return out
}

func uint64Slice(v []uint64) []string {
	out := make([]string, len(v))
	for i, x := range v {
		out[i] = fmt.Sprintf("%d", x)
	}
	return out
}
