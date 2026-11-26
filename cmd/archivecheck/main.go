package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"archivecheck/demo"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: archivecheck demo --case all --seed 17 --queue 2 --memory 67108864 --slow-ms 2")
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "archivecheck demo [--case all|normal|corrupt|recovery] [--seed N] [--queue N] [--memory BYTES] [--slow-ms MS] [--workdir DIR]")
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	caseName := fs.String("case", "all", "normal|corrupt|recovery|all")
	seed := fs.Int64("seed", 17, "deterministic fixture seed")
	queue := fs.Int("queue", 2, "fixed pipeline queue capacity")
	memory := fs.Int64("memory", 64<<20, "peak in-flight byte budget")
	slowMS := fs.Int("slow-ms", 0, "simulated slow consumer delay per record")
	workDir := fs.String("workdir", ".archivecheck-work", "local working directory")
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
	if *caseName != "all" && *caseName != "normal" && *caseName != "corrupt" && *caseName != "recovery" {
		fmt.Fprintf(stderr, "invalid --case %q\n", *caseName)
		return 2
	}
	if *queue <= 0 || *memory <= 0 || *slowMS < 0 {
		fmt.Fprintln(stderr, "--queue and --memory must be positive; --slow-ms must be non-negative")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts := demo.Options{
		Case: *caseName, Seed: *seed, Queue: *queue,
		Memory: *memory, SlowMS: *slowMS, WorkRoot: *workDir,
	}
	if err := demo.Run(ctx, opts, stdout); err != nil {
		var ve *demo.ValidationError
		if errors.As(err, &ve) {
			return 1
		}
		fmt.Fprintf(stderr, "archivecheck: %v\n", err)
		return 1
	}
	return 0
}
