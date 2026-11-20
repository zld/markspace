// jitterlab is a loopback voice-path jitter/PLC lab: it generates an
// impaired packet stream, receives it over UDP and/or TCP on
// 127.0.0.1, plays it out through a shared jitter-buffer controller,
// and reports waterline curves and summaries as JSON Lines.
//
// Exit codes: 0 ok, 2 bad arguments, 3 loopback I/O failure,
// 4 self-check invariant failure.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

const (
	exitOK        = 0
	exitArgs      = 2
	exitLoopback  = 3
	exitInvariant = 4
)

type options struct {
	transport string
	seed      int64
	duration  time.Duration
	frame     time.Duration
	curve     time.Duration
	format    string
}

func parseArgs(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.StringVar(&o.transport, "transport", "both", "udp, tcp, or both")
	fs.Int64Var(&o.seed, "seed", 1, "scenario random seed")
	fs.DurationVar(&o.duration, "duration", 8*time.Second, "run duration")
	fs.DurationVar(&o.frame, "frame", 20*time.Millisecond, "playout frame period")
	fs.DurationVar(&o.curve, "curve", 100*time.Millisecond, "waterline sample interval")
	fs.StringVar(&o.format, "format", "jsonl", "output format (jsonl)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch o.transport {
	case "udp", "tcp", "both":
	default:
		return o, fmt.Errorf("invalid --transport %q", o.transport)
	}
	if o.format != "jsonl" {
		return o, fmt.Errorf("invalid --format %q", o.format)
	}
	if o.duration <= 0 || o.frame <= 0 || o.curve <= 0 {
		return o, fmt.Errorf("durations must be positive")
	}
	if o.duration < o.frame {
		return o, fmt.Errorf("--duration must be >= --frame")
	}
	return o, nil
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "demo" {
		fmt.Fprintln(os.Stderr, "usage: jitterlab demo --transport udp|tcp|both --seed N --duration D --frame D --curve D --format jsonl")
		os.Exit(exitArgs)
	}
	o, err := parseArgs(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "jitterlab:", err)
		os.Exit(exitArgs)
	}
	code := run(o)
	os.Exit(code)
}
