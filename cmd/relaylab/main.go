// Command relaylab runs a reproducible loopback real-time media relay
// experiment: two Pion peers negotiate over a 127.0.0.1 signaling server,
// deterministic RTP traffic crosses a bounded relay with injected faults,
// and a JSON summary reports ICE, forwarding and jitter metrics.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"relaylab/internal/forward"
	"relaylab/internal/scenario"
	"relaylab/internal/signaling"
)

// Exit codes form the stable CLI contract documented in the acceptance spec.
const (
	exitOK             = 0
	exitArgOrSignaling = 2
	exitTransport      = 3
	exitShutdown       = 4
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitArgOrSignaling
	}
	switch args[0] {
	case "run":
		return runExperiment(args[1:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage(os.Stderr)
		return exitArgOrSignaling
	}
}

func runExperiment(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	duration := fs.Duration("duration", 5*time.Second, "experiment duration (max 30s)")
	packets := fs.Int("packets", 1200, "number of RTP packets to generate")
	queueSize := fs.Int("queue", 64, "bounded relay queue capacity")
	overflow := fs.String("overflow", "drop", "queue-full policy: drop|block")
	seed := fs.Int64("seed", 42, "deterministic fault-injection seed")
	asJSON := fs.Bool("json", true, "emit the JSON summary report")
	reorder := fs.Float64("reorder-rate", 0.08, "fraction of packets emitted out of order")
	duplicate := fs.Float64("duplicate-rate", 0.05, "fraction of packets duplicated")
	burstEvery := fs.Int("burst-every", 120, "burst delay fires every N packets (0 disables)")
	burstDelay := fs.Int64("burst-delay-ms", 30, "delay injected during each burst")
	shutdownTimeout := fs.Duration("shutdown-timeout", 3*time.Second, "grace period for clean teardown")

	if err := fs.Parse(args); err != nil {
		return exitArgOrSignaling
	}
	if err := validateFlags(*duration, *packets, *queueSize, *overflow, *reorder, *duplicate); err != nil {
		fmt.Fprintf(os.Stderr, "invalid arguments: %v\n", err)
		return exitArgOrSignaling
	}

	cfg := scenario.Config{
		Duration:        *duration,
		Packets:         *packets,
		QueueSize:       *queueSize,
		Overflow:        forward.OverflowMode(*overflow),
		Seed:            *seed,
		ReorderRate:     *reorder,
		BurstEvery:      *burstEvery,
		BurstDelayMS:    *burstDelay,
		DuplicateRate:   *duplicate,
		ShutdownTimeout: *shutdownTimeout,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	engine, err := scenario.NewEngine(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		return exitArgOrSignaling
	}
	report, runErr := engine.Run(ctx)

	// The report is always emitted, even after a failed phase, so operators
	// can distinguish negotiation failure from backpressure or transport loss.
	if *asJSON {
		if werr := report.WriteJSON(os.Stdout); werr != nil {
			fmt.Fprintf(os.Stderr, "report write failed: %v\n", werr)
		}
	}

	if !report.ShutdownClean {
		fmt.Fprintf(os.Stderr, "shutdown did not complete within %s\n", *shutdownTimeout)
		return exitShutdown
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "experiment failed: %v\n", runErr)
		return classifyError(runErr)
	}
	return exitOK
}

func validateFlags(duration time.Duration, packets, queue int, overflow string, reorder, duplicate float64) error {
	if duration <= 0 || duration > 30*time.Second {
		return fmt.Errorf("--duration must be within (0,30s]")
	}
	if packets <= 0 {
		return fmt.Errorf("--packets must be positive")
	}
	if queue <= 0 {
		return fmt.Errorf("--queue must be positive")
	}
	switch overflow {
	case "drop", "block":
	default:
		return fmt.Errorf("--overflow must be drop or block")
	}
	if reorder < 0 || reorder > 1 || duplicate < 0 || duplicate > 1 {
		return fmt.Errorf("rates must be within [0,1]")
	}
	return nil
}

// classifyError maps phase errors onto the signaling (2) vs transport (3)
// exit code contract.
func classifyError(err error) int {
	var pe *signaling.PhaseError
	if errors.As(err, &pe) {
		switch pe.Phase {
		case "signaling", "offer", "answer", "offer-delivery", "answer-delivery":
			return exitArgOrSignaling
		}
	}
	return exitTransport
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `relaylab — loopback real-time media relay experiment

Usage:
  relaylab run [flags]

Flags:
  --duration duration     experiment length, max 30s (default 5s)
  --packets int           RTP packets to generate (default 1200)
  --queue int             bounded relay capacity (default 64)
  --overflow string       drop|block when the queue is full (default drop)
  --seed int              deterministic fault seed (default 42)
  --json                  emit the JSON summary (default true)
  --reorder-rate float    fraction of packets reordered (default 0.05)
  --duplicate-rate float  fraction of packets duplicated (default 0.02)
  --burst-every int       burst delay period in packets (default 200)
  --burst-delay-ms int    burst delay duration (default 25)

Exit codes: 0 success, 2 argument/signaling error, 3 transport/timeout,
4 shutdown cleanup timeout.
`)
}
