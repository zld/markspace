package demo

import (
	"errors"
	"io"
	"os"
)

// ErrInjectedCrash is returned after the requested number of publishes.
var ErrInjectedCrash = errors.New("injected recovery crash")

// ErrCorruptInputs means all possible work completed but inputs were invalid.
var ErrCorruptInputs = errors.New("corrupt input samples")

// Config are the command-line demo settings.
type Config struct {
	Work              string
	Workers           int
	Queue             int
	ChunkBytes        int
	CrashAfterPublish int
	Stdout            io.Writer
}

func (c *Config) normalize() {
	if c.Workers <= 0 {
		c.Workers = 2
	}
	if c.Queue < 0 {
		c.Queue = 2
	}
	if c.ChunkBytes <= 0 {
		c.ChunkBytes = 1 << 20
	}
	if c.Stdout == nil {
		c.Stdout = os.Stdout
	}
}
