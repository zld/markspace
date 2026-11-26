// Command mediad is the terminal acceptance entry point for the local media
// processing kernel. The demo subcommand generates its own fixtures and
// exercises ingestion, planning, pause/resume, deduplication, recovery and
// corrupt-input rejection without any external service.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediad/internal/media"
	"mediad/internal/plan"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(media.ExitInvariantFailed)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(media.ExitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(media.ExitInvariantFailed)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediad demo --root DIR --seed N --max-bitrate B --budget-units U")
}

// demoFlags holds the parsed command line.
type demoFlags struct {
	root        string
	seed        int64
	maxBitrate  int64
	budgetUnits int64
}

func parseDemoFlags(args []string) (demoFlags, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var f demoFlags
	fs.StringVar(&f.root, "root", "", "writable demo working directory")
	fs.Int64Var(&f.seed, "seed", 23, "deterministic fixture seed")
	fs.Int64Var(&f.maxBitrate, "max-bitrate", 1_500_000, "bitrate cap in bps")
	fs.Int64Var(&f.budgetUnits, "budget-units", 900_000_000, "processing budget units")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if f.root == "" {
		return f, fmt.Errorf("--root is required")
	}
	return f, nil
}

func runDemo(args []string) int {
	f, err := parseDemoFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		return media.ExitInvariantFailed
	}
	d, err := newDemoEnv(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo setup:", err)
		return media.ExitInvariantFailed
	}

	// 1. Ingest the generated two-track timeline with one clip and a
	// cross-track transition.
	tl, err := d.ingest()
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo ingest:", err)
		return media.ClassifyExit(err)
	}

	// 2. Derive the transcoding configuration under the given constraints.
	decision, err := d.derive(tl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo plan:", err)
		// An empty feasible set is an input-constraint failure (exit 2),
		// not an internal invariant failure.
		if _, ok := err.(*plan.NoFeasibleCandidate); ok {
			return media.ExitInvalidInput
		}
		return media.ClassifyExit(err)
	}

	// 3. Run with a pause, then resume; verify the resume marker.
	first, err := d.runPaused(tl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo pause run:", err)
		return media.ClassifyExit(err)
	}
	second, err := d.runResume(tl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo resume run:", err)
		return media.ClassifyExit(err)
	}

	// 4. Re-run the completed task to prove idempotency and dedup.
	third, err := d.runResume(tl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo duplicate run:", err)
		return media.ClassifyExit(err)
	}

	// 5. Confirm the three corrupt fixtures each fail with a locatable
	// error and never produce a complete manifest.
	corrupt, err := d.verifyCorruptInputs()
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo corrupt check:", err)
		return media.ExitInvariantFailed
	}

	return d.report(tl, decision, first, second, third, corrupt)
}

// payloadBytes builds deterministic sample content from the seed.
func payloadBytes(seed int64, track string, idx, size int) []byte {
	b := make([]byte, size)
	base := byte(seed) + byte(track[0])
	for j := range b {
		b[j] = base + byte(idx*13+j*7)
	}
	return b
}

// candidates is the fixed ladder the planner chooses from.
func candidates(maxBitrate int64) []plan.Candidate {
	return []plan.Candidate{
		{Width: 1920, Height: 1080, Bitrate: 4_000_000, Quality: plan.Rational{Num: 60, Den: 100}, CostPerNs: 2},
		{Width: 1280, Height: 720, Bitrate: 2_500_000, Quality: plan.Rational{Num: 40, Den: 100}, CostPerNs: 1},
		{Width: 960, Height: 540, Bitrate: 1_500_000, Quality: plan.Rational{Num: 25, Den: 100}, CostPerNs: 1},
		{Width: 640, Height: 360, Bitrate: 800_000, Quality: plan.Rational{Num: 12, Den: 100}, CostPerNs: 1},
	}
}
