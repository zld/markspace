// Command regress generates local fixtures and audits a live multi-bitrate
// release: manifest timing, segment boundaries, keyframe indexes and lossless
// remuxing.
package main

import (
	"flag"
	"fmt"
	"os"

	"liveregress/internal/fault"
	"liveregress/internal/fixture"
	"liveregress/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  regress fixture --out DIR [--case all|clean|truncated|corrupt|no-index|clock-gap]
  regress check   --root DIR --case NAME [--recover] [--rebuild-index] [--out DIR]

Exit codes: 0 clean, 1 invalid mapping or untrustworthy parse, 2 recoverable defect.`)
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "fixture":
		return runFixture(args[1:])
	case "check":
		return runCheck(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return 1
	}
}

func runFixture(args []string) int {
	fs := flag.NewFlagSet("fixture", flag.ContinueOnError)
	out := fs.String("out", "", "output directory")
	caseName := fs.String("case", fixture.CaseAll, "case to generate (or all)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "fixture: --out is required")
		return 1
	}
	if !fixture.ValidCase(*caseName) {
		fmt.Fprintf(os.Stderr, "fixture: unknown case %q\n", *caseName)
		return 1
	}
	tree, err := fixture.Generate(*out, *caseName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		return 1
	}
	if *caseName == fixture.CaseAll {
		for _, c := range fixture.Cases {
			fmt.Printf("generated case=%s root=%s\n", c, *out+"/"+c)
		}
	} else {
		fmt.Printf("generated case=%s root=%s\n", *caseName, tree.Root)
	}
	return 0
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	root := fs.String("root", "", "input root")
	caseName := fs.String("case", "", "case name")
	recoverFlag := fs.Bool("recover", false, "resume from the last verified segment")
	rebuild := fs.Bool("rebuild-index", false, "rebuild missing I-frame indexes by scanning")
	out := fs.String("out", "", "output directory (default ROOT/.out)")
	maxSeg := fs.Int64("max-segment-bytes", 64<<20, "per-segment byte limit")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *root == "" || *caseName == "" {
		fmt.Fprintln(os.Stderr, "check: --root and --case are required")
		return 1
	}
	report, err := pipeline.Run(pipeline.Options{
		Root: *root, Case: *caseName, OutDir: *out,
		Recover: *recoverFlag, RebuildIndex: *rebuild, MaxSegmentBytes: *maxSeg,
	})
	if err != nil {
		code := fault.Code(err)
		fmt.Fprintf(os.Stderr, "check: %v\n", err)
		return code
	}
	report.Print(os.Stdout)
	return report.ExitCode()
}
