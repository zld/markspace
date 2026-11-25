// Command pack is the acceptance entry point for the media processing kernel.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediapack/internal/fault"
	"mediapack/internal/pipeline"
	"mediapack/internal/recovery"
	"mediapack/internal/report"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "rollback":
		os.Exit(runRollback(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: pack demo [--out DIR] [--corrupt-boundary] [--resume]")
	fmt.Fprintln(os.Stderr, "       pack rollback --out DIR")
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := fs.String("out", filepath.Join("/tmp", "game-pack"), "output directory")
	corrupt := fs.Bool("corrupt-boundary", false, "inject an MP4 boundary error")
	resume := fs.Bool("resume", false, "resume from the latest valid checkpoint")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	rep, err := pipeline.RunDemo(context.Background(), pipeline.Options{
		Out:             *out,
		CorruptBoundary: *corrupt,
		Resume:          *resume,
	})
	if rep != nil {
		_ = report.WriteJSON(os.Stdout, rep)
		fmt.Fprintln(os.Stdout)
	}
	if err != nil {
		if rep == nil {
			fr := &report.FailureReport{Kind: string(fault.Classify(err)), Message: err.Error(), ExitCode: fault.ExitCode(err)}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "failure": fr})
		}
		return fault.ExitCode(err)
	}
	return 0
}

func runRollback(args []string) int {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	out := fs.String("out", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "--out is required")
		return 1
	}
	manifest, err := recovery.Rollback(*out)
	if err != nil {
		fr := &report.FailureReport{Kind: string(fault.Classify(err)), Message: err.Error(), ExitCode: fault.ExitCode(err)}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "failure": fr})
		return fault.ExitCode(err)
	}
	fmt.Println(string(manifest))
	return 0
}
