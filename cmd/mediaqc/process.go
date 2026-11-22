package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"mediaqc/internal/qcrun"
)

// cmdProcess parses flags, runs one pipeline execution and prints the
// JSON summary to stdout.
func cmdProcess(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("process", flag.ContinueOnError)
	var (
		input         = fs.String("input", "", "input file")
		output        = fs.String("output", "", "output file")
		mapFlag       = fs.String("map", "", "track mapping src=dst,...")
		queue         = fs.Int("queue", 8, "queue capacity in packets")
		consumerDelay = fs.Duration("consumer-delay", 0, "simulated downstream latency")
		resume        = fs.Bool("resume", false, "resume from checkpoint state")
		maxSkewMs     = fs.Float64("max-skew-ms", 40, "max A/V skew in ms")
		cancelAfter   = fs.Int("cancel-after", 0, "selfcheck hook: cancel after N packets")
	)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *input == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "process: --input and --output are required")
		return 1
	}
	mapping, err := parseMapFlag(*mapFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "process: %v\n", err)
		return 2
	}
	sum, code := qcrun.Run(ctx, qcrun.Options{
		Input:         *input,
		Output:        *output,
		QueueCap:      *queue,
		ConsumerDelay: *consumerDelay,
		Resume:        *resume,
		Mapping:       mapping,
		MaxSkewNs:     int64(*maxSkewMs * 1e6),
		CancelAfter:   *cancelAfter,
	})
	out, _ := json.MarshalIndent(sum, "", "  ")
	fmt.Println(string(out))
	return code
}

// parseMapFlag parses "1=1,2=2" into a source->target mapping. A nil
// result means identity mapping over the discovered tracks.
func parseMapFlag(s string) (map[uint64]uint64, error) {
	if s == "" {
		return nil, nil
	}
	out := map[uint64]uint64{}
	var src, dst uint64
	for _, part := range splitComma(s) {
		if _, err := fmt.Sscanf(part, "%d=%d", &src, &dst); err != nil {
			return nil, fmt.Errorf("bad --map entry %q, want src=dst", part)
		}
		out[src] = dst
	}
	return out, nil
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
