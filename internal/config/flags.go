// Package config parses and validates command line parameters for rtspcheck.
package config

import (
	"errors"
	"flag"
	"fmt"
	"time"
)

// Transport identifiers exposed on the command line.
const (
	TransportUDP  = "udp"
	TransportTCP  = "tcp"
	TransportBoth = "both"
)

// Limits imposed on generated and buffered data so that malformed input or a
// misbehaving peer can never trigger unbounded memory growth.
const (
	MaxFrames        = 1024
	MaxWindowSize    = 256
	MaxPacketPayload = 1400
	MaxBufferedAUs   = 64
	DefaultTimeout   = 10 * time.Second
)

// Flags holds the validated parameters of one rtspcheck run.
type Flags struct {
	Command   string
	Transport string
	Frames    int
	Seed      int64
	Timeout   time.Duration
	Format    string
}

// Parse validates osArgs (normally os.Args[1:]) and returns a populated Flags.
// The returned error always carries a user-facing message; callers map it to
// exit code 2.
func Parse(osArgs []string) (*Flags, error) {
	if len(osArgs) == 0 {
		return nil, errors.New("missing command, expected \"demo\"")
	}

	f := &Flags{Command: osArgs[1:][0]}
	if f.Command != "demo" {
		return nil, fmt.Errorf("unknown command %q, only \"demo\" is supported", f.Command)
	}

	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	transport := fs.String("transport", TransportBoth, "transport to exercise: udp|tcp|both")
	frames := fs.Int("frames", 12, "number of source H.264 frames to generate (1..1024)")
	seed := fs.Int64("seed", 41, "deterministic seed for the generated sample stream")
	timeout := fs.Duration("timeout", DefaultTimeout, "maximum duration of one transport run")
	format := fs.String("format", "json", "output format: json")
	if err := fs.Parse(osArgs[1:]); err != nil {
		return nil, err
	}

	f.Transport = *transport
	f.Frames = *frames
	f.Seed = *seed
	f.Timeout = *timeout
	f.Format = *format

	if err := f.validate(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *Flags) validate() error {
	switch f.Transport {
	case TransportUDP, TransportTCP, TransportBoth:
	default:
		return fmt.Errorf("invalid transport %q: must be udp, tcp or both", f.Transport)
	}
	if f.Frames < 1 || f.Frames > MaxFrames {
		return fmt.Errorf("invalid frames %d: must be between 1 and %d", f.Frames, MaxFrames)
	}
	if f.Seed < 0 {
		return fmt.Errorf("invalid seed %d: must be non-negative", f.Seed)
	}
	if f.Timeout <= 0 {
		return fmt.Errorf("invalid timeout %s: must be positive", f.Timeout)
	}
	if f.Format != "json" {
		return fmt.Errorf("invalid format %q: only json is supported", f.Format)
	}
	return nil
}

// Transports returns the concrete transports to run in fixed order so that
// output ordering stays deterministic.
func (f *Flags) Transports() []string {
	if f.Transport == TransportBoth {
		return []string{TransportUDP, TransportTCP}
	}
	return []string{f.Transport}
}
