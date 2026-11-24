// Command tsrepair is the thin terminal driver for the reusable media
// repair library. It parses flags, generates the requested fixture, runs the
// engine and prints key=value metrics; all processing lives in the internal
// packages. Exit codes: 0 recoverable/clean, 1 internal error, 2 unsafe input.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/example/tsrepair/internal/engine"
	"github.com/example/tsrepair/internal/fixture"
	"github.com/example/tsrepair/internal/model"
)

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: tsrepair demo [flags]")
		return 1
	}
	opts, err := parseFlags(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "flag error: %v\n", err)
		return 1
	}
	stream := fixture.NewGenerator(opts.seed).Generate(opts.segmentPackets*2, opts.faults)
	cfg := engine.Config{
		QueueCapacity:      opts.queueCapacity,
		SegmentPackets:     opts.segmentPackets,
		Recover:            opts.recover,
		MaxBufferedPackets: opts.queueCapacity * opts.segmentPackets,
	}
	e := engine.New(cfg, stream)
	m, runErr := e.Run(context.Background())
	printMetrics(m)
	switch {
	case runErr == nil:
		return 0
	case model.IsFatal(runErr):
		fmt.Fprintf(os.Stderr, "fatal: %v\n", runErr)
		return 2
	default:
		fmt.Fprintf(os.Stderr, "internal: %v\n", runErr)
		return 1
	}
}

type options struct {
	seed           int64
	segmentPackets int
	queueCapacity  int
	recover        bool
	faults         []string
}

func parseFlags(args []string) (options, error) {
	o := options{seed: 1, segmentPackets: 20, queueCapacity: 2}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		need := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", arg)
			}
			i++
			return args[i], nil
		}
		switch {
		case arg == "--seed":
			v, err := need()
			if err != nil {
				return o, err
			}
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return o, fmt.Errorf("--seed: %w", err)
			}
			o.seed = n
		case arg == "--segment-packets":
			v, err := need()
			if err != nil {
				return o, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return o, fmt.Errorf("--segment-packets must be a positive int")
			}
			o.segmentPackets = n
		case arg == "--queue-capacity":
			v, err := need()
			if err != nil {
				return o, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return o, fmt.Errorf("--queue-capacity must be a positive int")
			}
			o.queueCapacity = n
		case arg == "--recover":
			o.recover = true
		case arg == "--faults":
			v, err := need()
			if err != nil {
				return o, err
			}
			o.faults = splitCSV(v)
			for _, f := range o.faults {
				if !knownFault(f) {
					return o, fmt.Errorf("unknown fault %q (valid: %s)", f,
						strings.Join(fixture.AllFaults, ","))
				}
			}
		default:
			return o, fmt.Errorf("unknown flag %q", arg)
		}
	}
	return o, nil
}

func splitCSV(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func knownFault(name string) bool {
	for _, f := range fixture.AllFaults {
		if f == name {
			return true
		}
	}
	return false
}

func printMetrics(m *engine.Metrics) {
	fmt.Printf("parse_roundtrip=%.2f%%\n", m.ParseRoundtripPct)
	fmt.Printf("keyframe_hit_rate=%.2f%%\n", m.KeyframeHitRatePct)
	fmt.Printf("segment_hash_match=%.2f%%\n", m.HashMatchPct)
	fmt.Printf("repaired_wraps=%d\n", m.RepairedWraps)
	fmt.Printf("ignored_duplicates=%d\n", m.IgnoredDuplicates)
	fmt.Printf("continuity_gaps=%d\n", m.ContinuityGaps)
	fmt.Printf("psi_versions=%d\n", m.PSIVersions)
	fmt.Printf("audio_errors=%d\n", m.AudioErrors)
	fmt.Printf("truncated_bytes=%d\n", m.TruncatedBytes)
	fmt.Printf("max_queue_depth=%d\n", m.MaxQueueDepth)
	fmt.Printf("segments=%d recovered_drops=%d packets_in=%d packets_accepted=%d\n",
		m.SegmentCount, m.RecoveredDrops, m.PacketsIn, m.PacketsAccepted)
}
