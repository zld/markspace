// Package config parses and validates rtspdiag command line parameters.
package config

import (
	"flag"
	"fmt"
	"io"
	"time"
)

// Exit codes shared by the CLI. Process-level values are defined in main.
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// Hard safety limits. Anomalous input must never cause unbounded buffering.
const (
	MaxWindowSize     = 4096
	MinWindowSize     = 2
	MaxTimeout        = 10 * time.Second
	MinTimeout        = time.Millisecond
	MaxPacketSize     = 65535 // RTP length field is 16 bits.
	DefaultPacketSize = 1500
)

// TransportMode selects the RTP transport used by the session.
type TransportMode string

const (
	TransportUDP TransportMode = "udp"
	TransportTCP TransportMode = "tcp"
)

// Valid reports whether m is a supported transport.
func (m TransportMode) Valid() bool {
	return m == TransportUDP || m == TransportTCP
}

// Config is the validated runtime configuration of one diagnostic run.
type Config struct {
	Transport  TransportMode
	Seed       int64
	Window     int
	Timeout    time.Duration
	JSONOutput bool
	// MaxPacketSize bounds RTP packet sizes accepted by the parser.
	MaxPacketSize int
}

// Parse converts raw arguments into a Config.
//
// Returns flag.ErrHelp when -h/-help was used so the caller can distinguish
// help requests from real usage errors.
func Parse(args []string) (*Config, error) {
	fs := flag.NewFlagSet("rtspdiag run", flag.ContinueOnError)
	// The CLI renders its own usage on stderr; silence the flag package output.
	fs.SetOutput(io.Discard)

	cfg := &Config{}
	var transport string
	var timeout string
	fs.StringVar(&transport, "transport", "udp", "RTP transport: udp or tcp")
	fs.Int64Var(&cfg.Seed, "seed", 42, "deterministic seed for the fixture generator")
	fs.IntVar(&cfg.Window, "window", 8, "RTP reorder window size in packets")
	fs.StringVar(&timeout, "timeout", "25ms", "reorder flush timeout (e.g. 25ms)")
	fs.BoolVar(&cfg.JSONOutput, "json", false, "emit a machine readable JSON report")
	fs.IntVar(&cfg.MaxPacketSize, "max-packet-size", DefaultPacketSize,
		"maximum accepted RTP packet size in bytes")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}

	cfg.Transport = TransportMode(transport)
	d, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, fmt.Errorf("invalid --timeout %q: %w", timeout, err)
	}
	cfg.Timeout = d

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate enforces value ranges and parameter combinations at startup so
// invalid boundaries fail before any session is opened.
func (c *Config) Validate() error {
	if !c.Transport.Valid() {
		return fmt.Errorf("invalid --transport %q: must be %q or %q",
			c.Transport, TransportUDP, TransportTCP)
	}
	if c.Window < MinWindowSize || c.Window > MaxWindowSize {
		return fmt.Errorf("invalid --window %d: must be between %d and %d",
			c.Window, MinWindowSize, MaxWindowSize)
	}
	if c.Timeout < MinTimeout || c.Timeout > MaxTimeout {
		return fmt.Errorf("invalid --timeout %s: must be between %s and %s",
			c.Timeout, MinTimeout, MaxTimeout)
	}
	if c.MaxPacketSize < 64 || c.MaxPacketSize > MaxPacketSize {
		return fmt.Errorf("invalid --max-packet-size %d: must be between 64 and %d",
			c.MaxPacketSize, MaxPacketSize)
	}
	return nil
}
