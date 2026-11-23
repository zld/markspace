// Command audit is the terminal front-end for the media processing kernel.
// It generates a deterministic fixture, runs the full audit pipeline and
// prints the acceptance metrics plus the manifest hash. Exit codes:
//
//	0 success
//	2 damaged input that was fully reported
//	1 parameter error or internal failure
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	fx "mediakernel/internal/fixture"
	"mediakernel/internal/mediaerr"
	"mediakernel/internal/pipeline"
)

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fixtureName := fs.String("fixture", "generated", "fixture source (only 'generated' is supported)")
	caseName := fs.String("case", "", "optional scenario filter (e.g. cancel)")
	workers := fs.Int("workers", 4, "worker pool size (1..64)")
	queue := fs.Int("queue", 16, "bounded queue capacity (1..4096)")
	lossBurst := fs.Int("loss-burst", 8, "synthetic RTP burst length")
	bandwidth := fs.String("bandwidth-kbps", "", "comma-separated bandwidth tiers in kbps")
	seed := fs.Int64("seed", 42, "deterministic RNG seed")
	outDir := fs.String("out", "", "output directory (defaults to a temp directory)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return pipeline.ExitFailure
	}

	tiers, err := parseBandwidth(*bandwidth)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parameter error:", err)
		return pipeline.ExitFailure
	}
	if *fixtureName != "generated" {
		fmt.Fprintf(os.Stderr, "parameter error: unsupported fixture %q\n", *fixtureName)
		return pipeline.ExitFailure
	}

	dir := *outDir
	if dir == "" {
		dir, err = os.MkdirTemp("", "audit-*")
		if err != nil {
			fmt.Fprintln(os.Stderr, "internal error:", err)
			return pipeline.ExitFailure
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "internal error:", err)
		return pipeline.ExitFailure
	}

	cfg := pipeline.Config{
		Workers: *workers, Queue: *queue, ReorderWindow: 16,
		LossBurst: *lossBurst, BandwidthKbps: tiers, Seed: *seed,
		OutputPath: filepath.Join(dir, "audit-remux.mp4"),
	}
	fixture := fx.NewGenerator(*seed).Build(*lossBurst, tiers)
	if *caseName == "damaged" {
		// Cross-stage fault: zero-timescale MPD fails at the parse stage.
		fixture.DASHMPD = fx.NewGenerator(*seed).DamagedDASH()
	}

	// The cancel scenario cancels immediately and must fully exit under 1s.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if *caseName == "cancel" {
		cancel()
	}

	done := make(chan struct {
		man  *pipeline.Manifest
		code int
		err  error
	}, 1)
	start := time.Now()
	go func() {
		man, code, err := pipeline.Run(ctx, cfg, fixture)
		done <- struct {
			man  *pipeline.Manifest
			code int
			err  error
		}{man, code, err}
	}()

	select {
	case r := <-done:
		return finish(r.man, r.code, r.err, start)
	case <-time.After(time.Second):
		fmt.Fprintln(os.Stderr, "internal error: pipeline did not exit within 1s")
		return pipeline.ExitFailure
	}
}

func finish(man *pipeline.Manifest, code int, err error, start time.Time) int {
	if err != nil {
		if me, ok := mediaerr.As(err); ok {
			fmt.Fprintf(os.Stderr, "parameter/internal error: %s (%s)\n", me.Code, me.Category)
		} else {
			fmt.Fprintln(os.Stderr, "internal error:", err)
		}
		return pipeline.ExitFailure
	}
	if man == nil {
		fmt.Fprintln(os.Stderr, "internal error: empty result")
		return pipeline.ExitFailure
	}

	m := man.Metrics
	switch code {
	case pipeline.ExitReported:
		fmt.Println("media audit finished with reported damage (no manifest committed)")
	default:
		fmt.Println("media audit complete")
	}
	fmt.Printf("  seed=%d elapsed_ms=%d\n", man.Seed, time.Since(start).Milliseconds())
	fmt.Printf("  av_sync_error_ms      = %d (target <=20)\n", m.AVSyncErrorMillis)
	fmt.Printf("  packet_recovery_rate  = %.4f (target >=0.90)\n", m.PacketRecoveryRate)
	fmt.Printf("  p95_latency_ms        = %d (target <=500)\n", m.P95LatencyMillis)
	fmt.Printf("  unrecovered_packets   = %d\n", m.UnrecoveredPackets)
	if m.ManifestSHA256 != "" {
		fmt.Printf("  manifest_sha256       = %s\n", m.ManifestSHA256)
	} else {
		fmt.Println("  manifest_sha256       = (not produced: input rejected)")
	}
	if man.Remux != nil {
		fmt.Printf("  remux                 = %s (%d bytes, verified=%v)\n",
			man.Remux.Path, man.Remux.Size, man.Remux.Verified)
	}
	if len(man.Errors) > 0 {
		fmt.Printf("  reported_errors       = %d\n", len(man.Errors))
		shown := man.Errors
		if len(shown) > 5 {
			shown = shown[:5]
		}
		for _, e := range shown {
			fmt.Printf("    - %s/%s track=%s recoverable=%v\n",
				e.Category, e.Code, e.TrackID, e.Recoverable)
		}
		if len(man.Errors) > 5 {
			fmt.Printf("    ... and %d more\n", len(man.Errors)-5)
		}
	}
	return code
}

func parseBandwidth(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("invalid bandwidth tier %q", p)
		}
		if v <= 0 {
			return nil, fmt.Errorf("bandwidth tier must be positive, got %d", v)
		}
		out = append(out, v)
	}
	return out, nil
}
