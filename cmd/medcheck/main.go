// Command medcheck is the thin terminal adapter for the reusable media
// processing libraries. It runs the deterministic acceptance demo and exits
// 0 only when every expected invariant passes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zld/medcheck/internal/demo"
	"github.com/zld/medcheck/pkg/media"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: medcheck demo --seed N --window N --out DIR")
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "usage: medcheck demo --seed 17 --window 8 --out DIR")
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	window := fs.Int("window", 8, "bounded RTP reorder/repair window")
	out := fs.String("out", filepath.Join(os.TempDir(), "media-evidence"), "local evidence output directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return 2
	}

	rep, err := demo.Run(demo.Options{Seed: *seed, Window: *window, Out: *out})
	if err != nil {
		fmt.Fprintln(stderr, "medcheck:", err)
		return media.ExitCode(err)
	}
	for _, line := range rep.TerminalLines() {
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(stdout, "evidence_json=%s\n", filepath.Join(*out, "evidence.json"))
	if !rep.OK() {
		fmt.Fprintln(stderr, "medcheck: one or more verification checks failed")
		return 1
	}
	return 0
}
