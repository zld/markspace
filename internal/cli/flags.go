// Package cli parses and validates command line arguments.
package cli

import (
	"flag"
	"fmt"
	"io"
)

// Config holds validated run parameters.
type Config struct {
	Mode     string // udp | tcp-interleaved | both
	Scenario string // stress | normal
	Seed     int64
	JSON     bool
}

// Modes expands the mode selector into concrete transports.
func (c Config) Modes() []string {
	if c.Mode == "both" {
		return []string{"udp", "tcp-interleaved"}
	}
	return []string{c.Mode}
}

// Parse parses args (excluding argv[0]) and validates the result.
func Parse(args []string, stderr io.Writer) (Config, error) {
	fs := flag.NewFlagSet("rtsp-audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "both", "transport: udp | tcp-interleaved | both")
	scenario := fs.String("scenario", "stress", "stream scenario: stress | normal")
	seed := fs.Int64("seed", 1, "deterministic stream seed")
	jsonOut := fs.Bool("json", false, "emit JSON summary instead of text")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}
	cfg := Config{Mode: *mode, Scenario: *scenario, Seed: *seed, JSON: *jsonOut}
	switch cfg.Mode {
	case "udp", "tcp-interleaved", "both":
	default:
		return Config{}, fmt.Errorf("invalid --mode %q: want udp, tcp-interleaved or both", cfg.Mode)
	}
	switch cfg.Scenario {
	case "stress", "normal":
	default:
		return Config{}, fmt.Errorf("invalid --scenario %q: want stress or normal", cfg.Scenario)
	}
	return cfg, nil
}
