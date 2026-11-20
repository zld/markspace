// mp4seek locates decodable GOP anchors in AVC MP4 files and exports
// sample manifests for archival pipelines.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"mp4seek/internal/avc"
	"mp4seek/internal/bmff"
	"mp4seek/internal/timeline"
)

// Exit codes.
const (
	exitOK      = 0
	exitUsage   = 2 // bad arguments or file I/O errors
	exitCorrupt = 3 // corrupt, conflicting, or unsupported container
	exitRange   = 4 // time out of range or no preceding IDR
)

var (
	errNoAnchor    = errors.New("no verified IDR at or before the requested time")
	errOutOfRange  = errors.New("requested time outside the presented range")
	errUnsupported = errors.New("unsupported container feature")
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usageText = `usage: mp4seek <command> [flags]

commands:
  fixture  --profile P --out F            generate a deterministic sample file
  inspect  --input F [--json]             print moov/trak summary
  index    --input F --track N --jsonl    expand the sample table as JSONL
  seek     --input F --track N --at-ms M  find the IDR anchor before M
  export   --input F --track N --from-ms A --to-ms B --out F
                                          write a JSONL sample manifest
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return exitUsage
	}
	var err error
	switch args[0] {
	case "fixture":
		err = cmdFixture(args[1:], stdout, stderr)
	case "inspect":
		err = cmdInspect(args[1:], stdout, stderr)
	case "index":
		err = cmdIndex(args[1:], stdout, stderr)
	case "seek":
		err = cmdSeek(args[1:], stdout, stderr)
	case "export":
		err = cmdExport(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n%s", args[0], usageText)
		return exitUsage
	}
	if err != nil {
		fmt.Fprintf(stderr, "mp4seek: %v\n", err)
		return exitCode(err)
	}
	return exitOK
}

func exitCode(err error) int {
	switch {
	case errors.Is(err, errNoAnchor), errors.Is(err, errOutOfRange):
		return exitRange
	case errors.Is(err, bmff.ErrTruncated), errors.Is(err, bmff.ErrCorrupt),
		errors.Is(err, bmff.ErrTooDeep), errors.Is(err, avc.ErrCorrupt),
		errors.Is(err, timeline.ErrUnsupportedRate), errors.Is(err, errUnsupported):
		return exitCorrupt
	default:
		return exitUsage
	}
}
