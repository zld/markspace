// Package cli implements the terminal commands. Each command returns the
// process exit code: 0 for success, 2 when the input was damaged but the
// damage was located, and 1 for parameter, runtime or verification failures.
package cli

import (
	"flag"
	"fmt"
	"io"
)

// commonFlags are shared by all commands.
type commonFlags struct {
	workdir string
	json    bool
	out     io.Writer
}

func newFlagSet(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	return fs
}

// parseArgs parses flags and rejects positional arguments, so typos are not
// silently ignored.
func parseArgs(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return nil
}
