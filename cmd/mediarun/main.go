// Command mediarun is the acceptance entry point for the deterministic media
// processing kernel. It runs entirely locally with no GUI or service deps.
//
// Exit codes: 0 success; 1 input/processing failure; 2 argument error.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediarun/internal/audio"
	"mediarun/internal/fixture"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "verify":
		return cmdVerify(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr,
		"mediarun - deterministic media processing kernel\n\n"+
			"usage:\n"+
			"  mediarun verify --self-fixtures --out <dir>\n"+
			"  mediarun run --fixture <kind> --checkpoint <dir> [--interrupt-after N]\n\n"+
			"exit codes: 0 ok, 1 input/processing failure, 2 argument error\n")
}

// thresholds encode the acceptance bounds from the specification.
type thresholds struct {
	avSyncMS   int64
	p95MS      int64
	recoveryOK func(float64) bool
}

func defaultThresholds() thresholds {
	return thresholds{
		avSyncMS:   20,
		p95MS:      2000,
		recoveryOK: func(r float64) bool { return r >= 0 && r <= 1 },
	}
}

func cmdVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	self := fs.Bool("self-fixtures", false, "generate and process built-in sample streams")
	out := fs.String("out", "", "output directory for artifacts and reports")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*self || *out == "" {
		fmt.Fprintln(os.Stderr, "verify requires --self-fixtures and --out <dir>")
		return 2
	}
	if err := prepareOut(*out); err != nil {
		fmt.Fprintf(os.Stderr, "output path rejected: %v\n", err)
		return 2
	}
	n, err := audio.NewNormalizer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ffmpeg unavailable: %v\n", err)
		return 1
	}
	ctx := context.Background()
	th := defaultThresholds()
	failed := false
	for _, kind := range fixture.AllKinds() {
		rep, err := verifyFixture(ctx, n, kind, filepath.Join(*out, kind))
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: processing failure: %v\n", kind, err)
			failed = true
			continue
		}
		printFixtureReport(rep, th)
		if !accept(rep, th) {
			failed = true
		}
	}
	// Demonstrate the go-mp4 timing/sample path on one real MP4 container.
	if err := demonstrateMP4(ctx, n, *out); err != nil {
		fmt.Fprintf(os.Stderr, "mp4 probe failure: %v\n", err)
		failed = true
	}
	if failed {
		return 1
	}
	fmt.Println("verify: all acceptance checks passed")
	return 0
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fix := fs.String("fixture", "", "fixture kind, e.g. damaged")
	checkpoint := fs.String("checkpoint", "", "checkpoint/work directory")
	interrupt := fs.Int("interrupt-after", 0, "stop after N segments (0 = run fully)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *fix == "" || *checkpoint == "" {
		fmt.Fprintln(os.Stderr, "run requires --fixture <kind> and --checkpoint <dir>")
		return 2
	}
	if !knownFixture(*fix) {
		fmt.Fprintf(os.Stderr, "unknown fixture %q; valid: %v\n", *fix, fixture.AllKinds())
		return 2
	}
	if *interrupt < 0 {
		fmt.Fprintln(os.Stderr, "--interrupt-after must be >= 0")
		return 2
	}
	if err := prepareOut(*checkpoint); err != nil {
		fmt.Fprintf(os.Stderr, "checkpoint path rejected: %v\n", err)
		return 2
	}
	n, err := audio.NewNormalizer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ffmpeg unavailable: %v\n", err)
		return 1
	}
	rep, err := executeRun(context.Background(), n, *fix, *checkpoint, *interrupt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "processing failure: %v\n", err)
		return 1
	}
	fmt.Println(rep.String())
	return 0
}

func knownFixture(kind string) bool {
	for _, k := range fixture.AllKinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// accept applies the per-scenario acceptance rules. The normal sample must be
// free of sync errors; every report must meet sync, latency and recovery bounds.
func accept(r *FixtureReport, th thresholds) bool {
	ok := true
	if r.Kind == fixture.KindNormal && r.SyncErrors != 0 {
		fmt.Fprintf(os.Stderr, "  FAIL: normal sample sync_errors=%d, want 0\n", r.SyncErrors)
		ok = false
	}
	if r.AVSyncMS > th.avSyncMS {
		fmt.Fprintf(os.Stderr, "  FAIL: av_sync_ms=%d > %d\n", r.AVSyncMS, th.avSyncMS)
		ok = false
	}
	if r.P95MS > th.p95MS {
		fmt.Fprintf(os.Stderr, "  FAIL: p95_ms=%d > %d\n", r.P95MS, th.p95MS)
		ok = false
	}
	if !r.ResumeMatches || !r.ProvenanceValid {
		fmt.Fprintf(os.Stderr, "  FAIL: resume_matches=%v provenance_valid=%v\n",
			r.ResumeMatches, r.ProvenanceValid)
		ok = false
	}
	if !th.recoveryOK(r.LossRecoveryRate) {
		fmt.Fprintf(os.Stderr, "  FAIL: loss_recovery_rate=%v out of [0,1]\n", r.LossRecoveryRate)
		ok = false
	}
	return ok
}

func printFixtureReport(r *FixtureReport, _ thresholds) {
	fmt.Printf("fixture=%s sync_errors=%d recovered_packets=%d loss_recovery_rate=%.3f "+
		"av_sync_ms=%d cache_hits=%d cache_misses=%d resume_matches=%t "+
		"provenance_valid=%t p95_ms=%d segments=%d\n",
		r.Kind, r.SyncErrors, r.RecoveredPackets, r.LossRecoveryRate, r.AVSyncMS,
		r.CacheHits, r.CacheMisses, r.ResumeMatches, r.ProvenanceValid, r.P95MS, r.Segments)
}

// prepareOut creates the output root and refuses any path containing traversal.
func prepareOut(dir string) error {
	for _, part := range splitParts(dir) {
		if part == ".." {
			return fmt.Errorf("path traversal rejected: %q", dir)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, 0o755)
}

func splitParts(p string) []string {
	return strings.Split(filepath.ToSlash(p), "/")
}
