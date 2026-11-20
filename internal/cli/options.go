// Package cli parses command-line options and centralises the loopback-only
// target policy and the process exit codes.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"time"
)

// Process exit codes. They are deliberately distinct so a calling pipeline
// can tell argument, protocol, media and timeout failures apart.
const (
	ExitOK             = 0
	ExitArgs           = 2
	ExitRTSPFailure    = 3
	ExitMediaInvalid   = 4
	ExitTimeoutOrFatal = 5
)

// DefaultTimeout bounds a single transport run.
const DefaultTimeout = 10 * time.Second

// Options is the parsed command line.
type Options struct {
	// Command is the verb, currently only "demo".
	Command string
	// Transport is one of "udp", "tcp" or "both".
	Transport string
	// Seed drives the deterministic fixture generator.
	Seed int64
	// JSON selects single-line JSON output on stdout.
	JSON bool
	// Timeout bounds each transport run.
	Timeout time.Duration
}

// Parse interprets argv (without the program name).
func Parse(argv []string) (Options, error) {
	if len(argv) == 0 {
		return Options{}, errors.New("missing command: expected 'demo'")
	}
	if argv[0] != "demo" {
		return Options{}, fmt.Errorf("unknown command %q: only 'demo' is supported", argv[0])
	}

	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	transport := fs.String("transport", "both", "media transport: udp|tcp|both")
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	asJSON := fs.Bool("json", false, "emit one stable JSON line per transport")
	timeout := fs.Duration("timeout", DefaultTimeout, "per-transport timeout")
	if err := fs.Parse(argv[1:]); err != nil {
		return Options{}, err
	}
	if fs.NArg() != 0 {
		return Options{}, fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}

	switch *transport {
	case "udp", "tcp", "both":
	default:
		return Options{}, fmt.Errorf("invalid --transport %q: want udp, tcp or both", *transport)
	}
	if *seed < 0 {
		return Options{}, fmt.Errorf("invalid --seed %d: must be non-negative", *seed)
	}

	return Options{
		Command:   "demo",
		Transport: *transport,
		Seed:      *seed,
		JSON:      *asJSON,
		Timeout:   *timeout,
	}, nil
}

// ValidateTargetURL enforces that the tool only talks to an RTSP service on
// the local loopback address. Any external host, non-rtsp scheme or parse
// error is rejected so no user-supplied URL can leave the machine.
func ValidateTargetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid target URL %q: %w", raw, err)
	}
	if u.Scheme != "rtsp" {
		return fmt.Errorf("target URL must use the rtsp:// scheme, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return fmt.Errorf("target host %q is not loopback; only 127.0.0.1 is allowed", host)
	}
	return nil
}

// Transports returns the ordered list of transports a run should execute.
// UDP always runs before TCP so report line order is stable.
func (o Options) Transports() []string {
	switch o.Transport {
	case "udp":
		return []string{"udp"}
	case "tcp":
		return []string{"tcp"}
	default:
		return []string{"udp", "tcp"}
	}
}
