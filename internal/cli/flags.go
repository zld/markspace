// Package cli wires command-line flags to the demo drivers and defines
// the process exit codes.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

// Exit codes.
const (
	ExitOK      = 0 // run completed
	ExitArgs    = 2 // invalid command line
	ExitCorrupt = 3 // input corrupt beyond recovery
	ExitIO      = 4 // I/O failure (socket, stdout)
)

// Options holds every demo flag.
type Options struct {
	Path      string // "trace" or "udp"
	Seed      int64
	Frames    int
	Loss      float64
	Burst     float64
	Reorder   float64
	Duplicate float64
	JitterMs  float64
	Curve     time.Duration
	Format    string // "text" or "csv"
	Listen    string
}

// Parse parses demo flags. Any problem is an argument error (exit 2).
func Parse(args []string) (Options, error) {
	var o Options
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Path, "path", "trace", "input path: trace or udp")
	fs.Int64Var(&o.Seed, "seed", 1, "generator seed")
	fs.IntVar(&o.Frames, "frames", 1000, "number of 10 ms frames to generate")
	fs.Float64Var(&o.Loss, "loss", 0, "independent loss probability [0,1]")
	fs.Float64Var(&o.Burst, "burst", 0, "probability that a loss extends into a burst [0,1]")
	fs.Float64Var(&o.Reorder, "reorder", 0, "probability of a 5-20 ms extra delay [0,1]")
	fs.Float64Var(&o.Duplicate, "duplicate", 0, "probability of a duplicate copy [0,1]")
	fs.Float64Var(&o.JitterMs, "jitter", 20, "uniform +/- arrival jitter in ms")
	fs.DurationVar(&o.Curve, "curve", 100*time.Millisecond, "queue-depth curve bucket size")
	fs.StringVar(&o.Format, "format", "text", "report format: text or csv")
	fs.StringVar(&o.Listen, "listen", "127.0.0.1:0", "UDP listen address (loopback only)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return o, o.Validate()
}

// Validate checks flag ranges.
func (o Options) Validate() error {
	if o.Path != "trace" && o.Path != "udp" {
		return fmt.Errorf("invalid --path %q (want trace or udp)", o.Path)
	}
	if o.Format != "text" && o.Format != "csv" {
		return fmt.Errorf("invalid --format %q (want text or csv)", o.Format)
	}
	if o.Frames < 1 || o.Frames > 100000 {
		return fmt.Errorf("--frames %d out of range [1,100000]", o.Frames)
	}
	for name, v := range map[string]float64{
		"--loss": o.Loss, "--burst": o.Burst,
		"--reorder": o.Reorder, "--duplicate": o.Duplicate,
	} {
		if v < 0 || v > 1 {
			return fmt.Errorf("%s %v out of range [0,1]", name, v)
		}
	}
	if o.JitterMs < 0 || o.JitterMs > 500 {
		return fmt.Errorf("--jitter %v out of range [0,500] ms", o.JitterMs)
	}
	if o.Curve < 10*time.Millisecond {
		return errors.New("--curve must be >= 10ms")
	}
	return nil
}
