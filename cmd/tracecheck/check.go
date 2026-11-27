package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"tracecheck/internal/ebml"
	"tracecheck/internal/mkv"
	"tracecheck/internal/serr"
)

// runCheck validates one Matroska/WebM file and reports its structure. It
// returns exit code 2 when the input is damaged but was reported, 1 on I/O or
// argument failure and 0 on a clean file.
func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: tracecheck check FILE\n")
		return 1
	}
	out := &output{w: stdout, err: stderr}
	data, err := readFile(fs.Arg(0))
	if err != nil {
		out.diag("IO_ERROR: %v", err)
		return 1
	}
	doc, perr := ebml.Parse(data)
	if perr != nil {
		reportError(out, perr)
		return 2
	}
	blocks, berr := mkv.ParseBlocks(doc)
	if berr != nil {
		reportError(out, berr)
		return 2
	}
	tracks, terr := mkv.ParseTracks(doc)
	if terr != nil {
		reportError(out, terr)
		return 2
	}
	out.key("input_bytes", len(data))
	out.key("blocks", len(blocks))
	out.key("tracks", len(tracks.Items))
	out.key("status", "ok")
	return 0
}

// reportError prints the structured code, offset and path of err.
func reportError(out *output, err error) {
	out.key("error_code", serr.CodeOf(err))
	out.key("error_offset", serr.OffsetOf(err))
	if se, ok := serr.As(err); ok {
		if len(se.Path) > 0 {
			out.key("error_path", joinPath(se.Path))
		}
		out.diag("%v", se)
	} else {
		out.diag("%v", err)
	}
}

// readFile reads a whole local file, wrapping failures as IO_ERROR.
func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, serr.New(serr.CodeIO, "read %s: %v", path, err)
	}
	return data, nil
}
