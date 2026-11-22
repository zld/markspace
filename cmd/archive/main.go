// Command archive runs the media-recovery kernel against locally generated
// MP4/RTP samples and emits an auditable JSON report plus terminal metrics.
//
// Exit codes:
//
//	0 - completed and all invariants passed
//	1 - internal error
//	2 - invalid input or manifest
//	3 - unrecoverable corruption
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/archive/mediakernel/internal/timeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "demo":
		if err := runDemo(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "archive: %v\n", err)
			os.Exit(exitCodeFor(err))
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: archive demo [flags]

Generates seed-driven MP4/RTP samples, recovers them through the pipeline and
writes a JSON report.

flags:
  --seed int             deterministic generator seed
  --duration duration    generated material duration (e.g. 1800s)
  --loss-burst int       contiguous packets dropped in transit
  --reorder-window int   RTP reorder window capacity
  --target-bitrate int   target segment bitrate in bps
  --out string           output JSON report path
  --corrupt-index        force an index digest mismatch and rebuild
  --resume               resume from the last completed segment
`)
}

// exitCodeFor maps the unified error taxonomy onto the documented codes.
func exitCodeFor(err error) int {
	switch timeline.Classify(err) {
	case timeline.KindInvalidInput, timeline.KindLimit:
		return 2
	case timeline.KindCorrupt:
		return 3
	default:
		return 1
	}
}

// durationFlag is a flag.Value accepting strings like "1800s" or "500ms".
type durationFlag struct{ seconds int }

func (d *durationFlag) String() string { return fmt.Sprintf("%ds", d.seconds) }
func (d *durationFlag) Set(v string) error {
	if len(v) == 0 {
		return fmt.Errorf("empty duration")
	}
	unit := v[len(v)-1]
	var numPart string
	var mult int
	switch unit {
	case 's':
		numPart, mult = v[:len(v)-1], 1
	case 'm':
		numPart, mult = v[:len(v)-1], 60
	case 'h':
		numPart, mult = v[:len(v)-1], 3600
	default:
		return fmt.Errorf("duration must end in s, m or h: %s", v)
	}
	n := 0
	for _, c := range numPart {
		if c < '0' || c > '9' {
			return fmt.Errorf("duration must be an integer: %s", v)
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return fmt.Errorf("duration must be positive")
	}
	d.seconds = n * mult
	return nil
}

var _ flag.Value = (*durationFlag)(nil)
