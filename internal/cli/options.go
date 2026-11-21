// Package cli parses and validates the looprelay command line.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	CommandDemo = "demo"

	DefaultListen    = "127.0.0.1:0"
	DefaultPackets   = 120
	DefaultInterval  = 20 * time.Millisecond
	DefaultTimeout   = 10 * time.Second
	DefaultQueueSize = 512

	MaxPackets = 100000
	MaxQueue   = 1000000
)

// UsageText is printed for help requests and invalid commands.
const UsageText = `Usage: looprelay demo [options]

Runs a self-contained loopback WebRTC verification pipeline:
synthetic RTP -> ingress link -> forwarder -> egress link -> metrics.

Options:
  --listen ADDR        signaling listen address (loopback IP, default 127.0.0.1:0)
  --packets N          synthetic packets to generate (default 120)
  --interval DURATION  nominal packet spacing (default 20ms)
  --drop-every N       skip every Nth packet, 0 disables (default 0)
  --delay-every N      delay every Nth packet, 0 disables (default 0)
  --delay DURATION     extra delay applied to marked packets (default 0s)
  --timeout DURATION   signaling/ICE negotiation timeout (default 10s)
  --queue-size N       forwarder bounded queue capacity (default 512)
  --json               emit stable JSON instead of a text summary
`

// ErrHelp is returned when the user asks for command help.
var ErrHelp = errors.New("help requested")

// Options holds the validated runtime configuration.
type Options struct {
	Command    string
	Listen     string
	Packets    int
	Interval   time.Duration
	DropEvery  int
	DelayEvery int
	Delay      time.Duration
	Timeout    time.Duration
	QueueSize  int
	JSON       bool
}

// Defaults returns the built-in demo configuration.
func Defaults() Options {
	return Options{
		Command:   CommandDemo,
		Listen:    DefaultListen,
		Packets:   DefaultPackets,
		Interval:  DefaultInterval,
		Timeout:   DefaultTimeout,
		QueueSize: DefaultQueueSize,
	}
}

// Parse parses arguments excluding the program name. The first argument must
// be a subcommand; only "demo" exists.
func Parse(args []string) (Options, error) {
	if len(args) == 0 {
		return Options{}, fmt.Errorf("missing command (expected %q)", CommandDemo)
	}
	switch args[0] {
	case "-h", "--help", "help":
		return Options{}, ErrHelp
	case CommandDemo:
	default:
		return Options{}, fmt.Errorf("unknown command %q (expected %q)", args[0], CommandDemo)
	}

	opts := Defaults()
	opts.Command = CommandDemo
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Listen, "listen", opts.Listen, "signaling listen address")
	fs.IntVar(&opts.Packets, "packets", opts.Packets, "packets to generate")
	fs.DurationVar(&opts.Interval, "interval", opts.Interval, "packet interval")
	fs.IntVar(&opts.DropEvery, "drop-every", opts.DropEvery, "drop every Nth packet")
	fs.IntVar(&opts.DelayEvery, "delay-every", opts.DelayEvery, "delay every Nth packet")
	fs.DurationVar(&opts.Delay, "delay", opts.Delay, "extra delay for marked packets")
	fs.DurationVar(&opts.Timeout, "timeout", opts.Timeout, "negotiation timeout")
	fs.IntVar(&opts.QueueSize, "queue-size", opts.QueueSize, "forwarder queue capacity")
	fs.BoolVar(&opts.JSON, "json", opts.JSON, "emit JSON")

	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Options{}, ErrHelp
		}
		return Options{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if fs.NArg() != 0 {
		return Options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate checks every option and rejects configurations that cannot produce
// a meaningful loopback run.
func (o Options) Validate() error {
	if err := validateListen(o.Listen); err != nil {
		return err
	}
	if o.Packets < 1 || o.Packets > MaxPackets {
		return fmt.Errorf("--packets must be in [1,%d], got %d", MaxPackets, o.Packets)
	}
	if o.Interval < time.Millisecond || o.Interval > 5*time.Second {
		return fmt.Errorf("--interval must be in [1ms,5s], got %s", o.Interval)
	}
	if o.DropEvery < 0 || o.DropEvery == 1 {
		return fmt.Errorf("--drop-every must be 0 or at least 2, got %d", o.DropEvery)
	}
	if o.DropEvery > o.Packets && o.DropEvery != 0 {
		return fmt.Errorf("--drop-every %d exceeds --packets %d", o.DropEvery, o.Packets)
	}
	if o.DelayEvery < 0 || o.DelayEvery == 1 {
		return fmt.Errorf("--delay-every must be 0 or at least 2, got %d", o.DelayEvery)
	}
	if o.Delay < 0 || o.Delay > time.Second {
		return fmt.Errorf("--delay must be in [0s,1s], got %s", o.Delay)
	}
	if o.DelayEvery > 0 && o.Delay == 0 {
		return fmt.Errorf("--delay must be positive when --delay-every is set")
	}
	if o.Timeout < 100*time.Millisecond || o.Timeout > time.Minute {
		return fmt.Errorf("--timeout must be in [100ms,1m], got %s", o.Timeout)
	}
	if o.QueueSize < 1 || o.QueueSize > MaxQueue {
		return fmt.Errorf("--queue-size must be in [1,%d], got %d", MaxQueue, o.QueueSize)
	}
	return nil
}

func validateListen(addr string) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--listen must be host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen must use a loopback IP, got %q", host)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("--listen has invalid port %q", portText)
	}
	return nil
}
