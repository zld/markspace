// Package config parses and validates CLI parameters for relaydiag.
package config

import (
	"flag"
	"fmt"
	"io"
	"net"
	"time"
)

// Hard limits that bound a single run regardless of user input.
const (
	MaxPackets  = 1_000_000
	MaxInterval = time.Second
	MaxTimeout  = 5 * time.Minute
)

// Config holds the validated parameters of a `run` invocation.
type Config struct {
	Address  string        // loopback address both ends bind to
	Packets  int           // number of RTP packets to generate
	Interval time.Duration // delay between generated packets
	Timeout  time.Duration // overall run deadline
	JSON     bool          // emit JSON summary on stdout
}

// Parse parses run-subcommand arguments and validates them. On any error
// the flag set has already printed usage to usageOut.
func Parse(args []string, usageOut io.Writer) (Config, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(usageOut)

	cfg := Config{}
	fs.StringVar(&cfg.Address, "address", "127.0.0.1", "loopback address for both peers (127.0.0.1 or ::1)")
	fs.IntVar(&cfg.Packets, "packets", 100, "number of RTP packets to generate (1..1000000)")
	fs.DurationVar(&cfg.Interval, "interval", 5*time.Millisecond, "interval between generated packets")
	fs.DurationVar(&cfg.Timeout, "timeout", 20*time.Second, "overall run timeout")
	fs.BoolVar(&cfg.JSON, "json", false, "print JSON summary to stdout")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(usageOut, "invalid parameters: %v\n", err)
		fs.Usage()
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects illegal parameter combinations.
func (c Config) Validate() error {
	if c.Packets <= 0 {
		return fmt.Errorf("packets must be > 0, got %d", c.Packets)
	}
	if c.Packets > MaxPackets {
		return fmt.Errorf("packets must be <= %d, got %d", MaxPackets, c.Packets)
	}
	if c.Interval <= 0 {
		return fmt.Errorf("interval must be > 0, got %s", c.Interval)
	}
	if c.Interval > MaxInterval {
		return fmt.Errorf("interval must be <= %s, got %s", MaxInterval, c.Interval)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("timeout must be > 0, got %s", c.Timeout)
	}
	if c.Timeout > MaxTimeout {
		return fmt.Errorf("timeout must be <= %s, got %s", MaxTimeout, c.Timeout)
	}
	ip := net.ParseIP(c.Address)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("address must be a loopback IP (127.0.0.1 or ::1), got %q", c.Address)
	}
	return nil
}
