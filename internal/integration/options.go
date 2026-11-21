package integration

import (
	"time"

	"looprelay/internal/cli"
)

// Options is the runtime configuration the runner consumes.
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

// FromCLI converts validated CLI options.
func FromCLI(c cli.Options) Options {
	return Options{
		Command:    c.Command,
		Listen:     c.Listen,
		Packets:    c.Packets,
		Interval:   c.Interval,
		DropEvery:  c.DropEvery,
		DelayEvery: c.DelayEvery,
		Delay:      c.Delay,
		Timeout:    c.Timeout,
		QueueSize:  c.QueueSize,
		JSON:       c.JSON,
	}
}
