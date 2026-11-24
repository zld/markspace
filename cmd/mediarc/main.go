// Command mediarc is the terminal acceptance adapter for MediaRC.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/zld/mediarc/internal/model"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		var pe *model.ParseError
		if errors.As(err, &pe) && pe != nil {
			fmt.Printf("byte_offset=%d\n", pe.ByteOffset)
		}
		fmt.Fprintf(os.Stderr, "mediarc: %v\n", err)
		os.Exit(model.ExitCode(err))
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return &model.ConfigError{Field: "command", Err: errors.New("expected demo or verify")}
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return nil
	default:
		return &model.ConfigError{Field: "command", Value: args[0], Err: errors.New("unknown command")}
	}
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, `usage:
  mediarc demo --seed 17 --out DIR
  mediarc demo --corrupt truncated-box --out DIR
  mediarc demo --late-event --checkpoint DIR/checkpoint.json
  mediarc verify --input DIR`)
}

type demoOptions struct {
	seed       int64
	out        string
	corrupt    string
	lateEvent  bool
	checkpoint string
}

func parseDemoFlags(args []string) (demoOptions, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var o demoOptions
	fs.Int64Var(&o.seed, "seed", 17, "deterministic fixture seed")
	fs.StringVar(&o.out, "out", "", "output directory")
	fs.StringVar(&o.corrupt, "corrupt", "", "corruption mode: truncated-box")
	fs.BoolVar(&o.lateEvent, "late-event", false, "recover from checkpoint with a late event")
	fs.StringVar(&o.checkpoint, "checkpoint", "", "checkpoint path for late-event recovery")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return o, &model.ConfigError{Field: "flags", Err: err}
	}
	if o.lateEvent {
		if o.checkpoint == "" {
			return o, &model.ConfigError{Field: "checkpoint", Err: errors.New("required with --late-event")}
		}
		return o, nil
	}
	if o.out == "" {
		return o, &model.ConfigError{Field: "out", Err: errors.New("required")}
	}
	if o.corrupt != "" && o.corrupt != "truncated-box" {
		return o, &model.ConfigError{Field: "corrupt", Value: o.corrupt, Err: errors.New("unsupported corruption mode")}
	}
	return o, nil
}
