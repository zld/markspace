package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"archivist/internal/fault"
	"archivist/internal/pipeline"
)

// cmdCheck processes exactly one input and exits with the documented contract
// codes, so a shell script can assert on them directly: 0 ok, 2 corrupt or
// unreparable input, 1 any other failure.
func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "./tmp/check", "work directory")
	queue := fs.Int("queue", 1, "maximum inputs in flight")
	chunk := fs.Int("chunk", 1, "samples per chunk")
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "archivist: check requires exactly one input file")
		return exitFailure
	}
	r, err := pipeline.New(pipeline.Options{
		QueueCapacity: *queue,
		WorkDir:       *out,
		ChunkSamples:  *chunk,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "archivist: %v\n", err)
		return fault.ExitCode(err)
	}
	results, runErr := r.Run(context.Background(), fs.Args())
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if len(results) > 0 {
		if err := enc.Encode(results[0]); err != nil {
			fmt.Fprintf(os.Stderr, "archivist: encode result: %v\n", err)
			return exitFailure
		}
	}
	if runErr != nil {
		var stop *pipeline.ErrInterrupted
		if errors.As(runErr, &stop) {
			return exitOK
		}
		fmt.Fprintf(os.Stderr, "archivist: %v\n", runErr)
		return fault.ExitCode(runErr)
	}
	return exitOK
}
