// Command mediacheck converts local RTP/RTCP captures into a uniform MP4
// delivery with a verifiable manifest and quality report.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediaforge/internal/fixture"
)

const usage = `mediacheck - RTP/RTCP audio delivery checker

Usage:
  mediacheck generate --out <dir>
  mediacheck run      --input <capture.rtp> [options] --out <dir>
  mediacheck verify   --dir <dir>

Exit codes: 0 success, 1 input/verification failure, 2 usage error.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

// run dispatches a subcommand and returns the process exit code.
func run(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "generate":
		return cmdGenerate(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "verify":
		return cmdVerify(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", args[0], usage)
		return 2
	}
}

// usageError prints a parameter problem and returns exit code 2.
func usageError(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "mediacheck: "+format+"\n", args...)
	return 2
}

func cmdGenerate(args []string) int {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "output directory for fixtures")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		return usageError("generate requires --out")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mediacheck: cannot create %s: %v\n", *out, err)
		return 1
	}
	if err := fixture.Generate(*out); err != nil {
		fmt.Fprintf(os.Stderr, "mediacheck: generate failed: %v\n", err)
		return 1
	}
	for _, name := range fixture.All() {
		path := filepath.Join(*out, name)
		st, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mediacheck: %s missing after generate\n", path)
			return 1
		}
		fmt.Printf("generated %s (%d bytes)\n", path, st.Size())
	}
	return 0
}
