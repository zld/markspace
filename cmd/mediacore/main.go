// Command mediacore is the terminal acceptance adapter for the local media
// processing kernel. It exposes one demo workflow that generates its own
// fixtures (normal plus four corruption scenarios), runs the bounded
// pipeline and prints the evidence report.
package main

import (
	"fmt"
	"os"
	"time"

	"mediacore/internal/clock"
	"mediacore/internal/testkit"
)

const usage = `mediacore - local low-latency media processing kernel

usage:
  mediacore demo --duration 3600s --target-bps 1800000 \
                 --batch-bytes 1048576 --slow-every 7 --fixtures generated

flags:
  --duration       logical manifest timeline span (Go duration, e.g. 3600s)
  --target-bps     target bitrate in bits per second
  --batch-bytes    hard per-batch physical byte cap
  --slow-every     synthetic slow consumer stalls every Nth batch (0 = off)
  --fixtures       output directory for generated assets and caches

exit codes:
  0 success | 2 recoverable corruption/cancel | 3 bad arguments | 4 invariant
`

// DemoArgs is the parsed and validated demo configuration.
type DemoArgs struct {
	Duration   time.Duration
	TargetBPS  uint64
	BatchBytes int64
	SlowEvery  int
	Fixtures   string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(clock.KindInvalidArgument.ExitCode())
	}
	switch os.Args[1] {
	case "demo":
		args, err := parseDemoArgs(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "argument error: %v\n\n%s", err, usage)
			os.Exit(clock.KindInvalidArgument.ExitCode())
		}
		code, err := runDemo(args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "demo failed: %v\n", err)
		}
		os.Exit(code)
	case "-h", "--help", "help":
		fmt.Fprint(os.Stderr, usage)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", os.Args[1], usage)
		os.Exit(clock.KindInvalidArgument.ExitCode())
	}
}

// runDemo returns the process exit code alongside any surfaced error.
func runDemo(args DemoArgs) (int, error) {
	root, err := testkit.PrepareRoot(args.Fixtures)
	if err != nil {
		return clock.Classify(err).ExitCode(), err
	}
	fmt.Printf("mediacore demo: root=%s duration=%s target_bps=%d\n",
		root, args.Duration, args.TargetBPS)

	params := testkit.DemoParams{
		Duration:   args.Duration,
		TargetBPS:  args.TargetBPS,
		BatchBytes: args.BatchBytes,
		SlowEvery:  args.SlowEvery,
	}
	scenarios, err := testkit.GenerateScenarios(root, params)
	if err != nil {
		return clock.Classify(err).ExitCode(), err
	}

	var normal *testkit.PipelineResult
	for _, sc := range scenarios {
		fmt.Printf("\n== scenario: %s ==\n", sc.Name)
		res, err := testkit.RunScenario(sc, params)
		if err != nil {
			// Recoverable scenarios are expected demo outcomes, not process
			// failures: report and continue.
			fmt.Printf("status=recoverable_error kind=%d detail=%v\n",
				clock.Classify(err), err)
			continue
		}
		fmt.Println(res.Report.Render())
		if sc.Name == "normal" {
			normal = res
		}
	}
	if normal == nil {
		return clock.KindRecoverable.ExitCode(),
			clock.ErrRecoverablef("demo", "normal scenario did not complete")
	}
	if err := testkit.AssertAcceptance(normal, testkit.DefaultAcceptance()); err != nil {
		// An acceptance-bound violation on self-calibrated input is an
		// internal invariant failure.
		return clock.KindInvariant.ExitCode(), err
	}
	fmt.Println("\nacceptance=all_bounds_satisfied exit=0")
	return 0, nil
}
