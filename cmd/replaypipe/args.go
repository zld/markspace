package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"replaypipe/internal/clip"
)

// config is the parsed command line for `verify`.
type config struct {
	scenario     string
	root         string
	pre          int64
	post         int64
	minWidth     int
	minFPS       float64
	maxWorkBytes uint64
	maxOutBytes  uint64
	verbose      bool
}

func parseArgs(args []string) (*config, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("missing subcommand (expected 'verify')")
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		return nil, fmt.Errorf("usage: replaypipe verify --scenario all --root DIR [flags]")
	}
	if args[0] != "verify" {
		return nil, fmt.Errorf("unknown subcommand %q (expected 'verify')", args[0])
	}
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfg := &config{}
	pre := fs.String("pre", "5s", "pre-roll window before each event")
	post := fs.String("post", "8s", "post-roll window after each event")
	fs.StringVar(&cfg.scenario, "scenario", "all", "scenario: all|normal|envelope|event|corrupt|migrate")
	fs.StringVar(&cfg.root, "root", "", "work directory for generated fixtures and outputs")
	fs.IntVar(&cfg.minWidth, "min-width", 1280, "minimum video width")
	fs.Float64Var(&cfg.minFPS, "min-fps", 30, "minimum frame rate")
	workBytes := fs.String("max-work-bytes", "67108864", "processing budget in bytes")
	outBytes := fs.String("max-output-bytes", "16777216", "delivery size ceiling in bytes")
	fs.BoolVar(&cfg.verbose, "verbose", false, "print per-slice details")
	if err := fs.Parse(args[1:]); err != nil {
		return nil, err
	}
	if cfg.root == "" {
		return nil, fmt.Errorf("--root is required")
	}
	var err error
	if cfg.pre, err = parseDurationNS(*pre); err != nil {
		return nil, fmt.Errorf("--pre: %w", err)
	}
	if cfg.post, err = parseDurationNS(*post); err != nil {
		return nil, fmt.Errorf("--post: %w", err)
	}
	if cfg.maxWorkBytes, err = parseBytes(*workBytes); err != nil {
		return nil, fmt.Errorf("--max-work-bytes: %w", err)
	}
	if cfg.maxOutBytes, err = parseBytes(*outBytes); err != nil {
		return nil, fmt.Errorf("--max-output-bytes: %w", err)
	}
	if cfg.pre < 0 || cfg.post < 0 {
		return nil, fmt.Errorf("--pre/--post must not be negative")
	}
	if cfg.minWidth < 0 || cfg.minFPS < 0 {
		return nil, fmt.Errorf("--min-width/--min-fps must not be negative")
	}
	switch cfg.scenario {
	case "all", "normal", "envelope", "event", "corrupt", "migrate":
	default:
		return nil, fmt.Errorf("--scenario %q is not one of all|normal|envelope|event|corrupt|migrate", cfg.scenario)
	}
	return cfg, nil
}

func (c *config) budget() clip.Budget {
	return clip.Budget{
		PreNS: c.pre, PostNS: c.post,
		MinWidth: uint16(c.minWidth), MinFPS: c.minFPS,
		MaxWorkBytes: c.maxWorkBytes, MaxOutBytes: c.maxOutBytes,
	}
}

// parseDurationNS accepts Go durations plus an explicit "<n>ns" form.
func parseDurationNS(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(s, "ns") {
		v, err := strconv.ParseInt(strings.TrimSuffix(s, "ns"), 10, 64)
		if err != nil {
			return 0, err
		}
		return v, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return int64(d), nil
}

// parseBytes accepts plain integers and KiB/MiB/GiB suffixes.
func parseBytes(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty byte count")
	}
	mult := uint64(1)
	for suffix, m := range map[string]uint64{"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "K": 1 << 10, "M": 1 << 20, "G": 1 << 30} {
		if strings.HasSuffix(s, suffix) {
			mult = m
			s = strings.TrimSuffix(s, suffix)
			break
		}
	}
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	if v > (1<<63-1)/mult {
		return 0, fmt.Errorf("byte count overflows uint64")
	}
	return v * mult, nil
}
