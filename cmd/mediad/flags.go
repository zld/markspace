package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// demoFlags are the documented acceptance switches.
type demoFlags struct {
	seed            int64
	loss            float64
	chunkSize       int
	report          string
	stopAfterChunks int
	workDir         string
}

// newDemoFlagSet binds the documented switches onto f.
func newDemoFlagSet(f *demoFlags, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Int64Var(&f.seed, "seed", 7, "deterministic generation seed")
	fs.Float64Var(&f.loss, "loss", 0.04, "fraction of RTP packets to drop (0..1)")
	fs.IntVar(&f.chunkSize, "chunk-size", 64, "segments per checkpointed work chunk")
	fs.StringVar(&f.report, "report", "stdout", "report destination: stdout or a file path")
	fs.IntVar(&f.stopAfterChunks, "stop-after-chunks", 0, "commit N new chunks then stop (resume testing)")
	fs.StringVar(&f.workDir, "work-dir", ".mediad-work", "directory for index, cache and checkpoint state")
	return fs
}

func parseDemoFlags(args []string, stderr io.Writer) (demoFlags, error) {
	var f demoFlags
	fs := newDemoFlagSet(&f, stderr)
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() != 0 {
		return f, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	switch {
	case f.loss < 0 || f.loss > 1:
		return f, fmt.Errorf("loss must be within [0,1], got %v", f.loss)
	case f.chunkSize <= 0:
		return f, fmt.Errorf("chunk-size must be positive, got %d", f.chunkSize)
	case f.stopAfterChunks < 0:
		return f, fmt.Errorf("stop-after-chunks must not be negative")
	}
	return f, nil
}
