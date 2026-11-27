// Command mediad is the terminal entry point of the media kernel.
//
// Subcommands:
//
//	mediad verify --generate-fixtures --work DIR --budget-ms N --max-bytes N
//	mediad parse --input FILE
//
// Exit codes: 0 success, 2 parameter or input error, 1 internal error.
package main

import (
	"flag"
	"fmt"
	"os"

	"mediad/internal/clock"
	"mediad/internal/ebml"
)

const (
	exitOK       = 0
	exitInternal = 1
	exitUsage    = 2
)

// maxFileSize caps media files loaded into memory (256 MiB).
const maxFileSize = 256 << 20

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mediad <verify|parse> [flags]")
		return exitUsage
	}
	switch args[0] {
	case "verify":
		fs := flag.NewFlagSet("verify", flag.ContinueOnError)
		gen := fs.Bool("generate-fixtures", false, "generate sample fixtures first")
		work := fs.String("work", "", "work directory")
		budget := fs.Int64("budget-ms", 5000, "processing budget in ms")
		maxBytes := fs.Int64("max-bytes", 1<<20, "deliverable size cap in bytes")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *work == "" {
			fmt.Fprintln(os.Stderr, "verify: --work is required")
			return exitUsage
		}
		if err := verify(*work, *gen, *budget, *maxBytes); err != nil {
			fmt.Fprintf(os.Stderr, "verify: %v\n", err)
			return exitInternal
		}
		return exitOK
	case "parse":
		fs := flag.NewFlagSet("parse", flag.ContinueOnError)
		input := fs.String("input", "", "media file to parse")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *input == "" {
			fmt.Fprintln(os.Stderr, "parse: --input is required")
			return exitUsage
		}
		return parseFileCmd(*input)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", args[0])
		return exitUsage
	}
}

// parseFileCmd parses one media file and reports events or a localized
// error. Corrupt input exits 2 with stable diagnostics.
func parseFileCmd(path string) int {
	data, err := readCapped(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error_code=%s msg=%v\n", clock.ErrBounds, err)
		return exitUsage
	}
	nodes, err := ebml.NewParser(data).Parse()
	if err != nil {
		if ce, ok := err.(*clock.Error); ok {
			printClockError(ce)
			return exitUsage
		}
		fmt.Fprintf(os.Stderr, "internal: %v\n", err)
		return exitInternal
	}
	var seg *ebml.Node
	for _, n := range nodes {
		if n.ID == ebml.IDSegment {
			seg = n
		}
	}
	if seg == nil {
		fmt.Fprintf(os.Stderr, "error_code=%s msg=no segment\n", clock.ErrBounds)
		return exitUsage
	}
	events, err := ebml.Events(seg)
	if err != nil {
		if ce, ok := err.(*clock.Error); ok {
			printClockError(ce)
			return exitUsage
		}
		fmt.Fprintf(os.Stderr, "internal: %v\n", err)
		return exitInternal
	}
	fmt.Printf("events=%d\n", len(events))
	return exitOK
}

// printClockError prints the stable corrupt-input diagnostics.
func printClockError(e *clock.Error) {
	fmt.Printf("error_code=%s byte_offset=%d element_path=%s\n",
		e.Code, e.Offset, e.Path)
}

// readCapped loads a file enforcing the memory cap.
func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > maxFileSize {
		return nil, clock.New(clock.ErrSizeOverflow,
			"file %d bytes exceeds cap %d", st.Size(), maxFileSize)
	}
	data := make([]byte, st.Size())
	if _, err := f.ReadAt(data, 0); err != nil {
		return nil, err
	}
	return data, nil
}
