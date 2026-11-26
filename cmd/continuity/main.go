package main

import (
	"flag"
	"fmt"
	"os"

	"continuity/internal/acceptance"
	"continuity/internal/media"
	"continuity/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "verify":
		verifyCmd(os.Args[2:])
	case "run":
		runCmd(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func verifyCmd(args []string) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	caseName := fs.String("case", "", "clean|damaged|boundary|recovery")
	out := fs.String("out", "", "output directory (defaults to /tmp/continuity-<case>)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if *caseName == "" {
		fmt.Fprintln(os.Stderr, "--case is required")
		os.Exit(2)
	}
	if err := acceptance.Verify(*caseName, *out); err != nil {
		fmt.Fprintf(os.Stderr, "verify_failure: %v\n", err)
		os.Exit(1)
	}
}

func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	caseName := fs.String("case", "", "clean|recovery|damaged|boundary")
	root := fs.String("root", "", "sample root (defaults to <out>/sample)")
	out := fs.String("out", "", "publication output directory")
	injectCrash := fs.Bool("inject-crash", false, "exit 75 after durable artifacts and before state commit")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	res, err := pipeline.Run(pipeline.Options{Case: *caseName, Root: *root, Out: *out, InjectCrash: *injectCrash})
	if err != nil {
		f, ok := media.AsFault(err)
		if ok {
			fmt.Fprintf(os.Stderr, "run_rejected code=%s phase=%s error=%q\n", f.Code, f.Phase, f.Error())
		} else {
			fmt.Fprintf(os.Stderr, "run_failed: %v\n", err)
		}
		os.Exit(media.ExitCode(err))
	}
	for _, line := range pipeline.MetricLines(res) {
		fmt.Println(line)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: continuity <verify|run> [flags]")
	fmt.Fprintln(os.Stderr, "  verify --case clean|damaged|boundary|recovery [--out DIR]")
	fmt.Fprintln(os.Stderr, "  run --case NAME [--root DIR] --out DIR [--inject-crash]")
}
