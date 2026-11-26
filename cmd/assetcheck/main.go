package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"assetcheck/internal/media"
)

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(stderr, "usage: assetcheck demo --dir DIR --case CASE [--redeliver N]")
		return 1
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "local working directory")
	caseName := fs.String("case", "clean", "fixture/demo case")
	redeliver := fs.Int("redeliver", 2, "concurrent duplicate deliveries")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if *dir == "" {
		fmt.Fprintln(stderr, "--dir is required")
		return 1
	}
	if err := runDemo(stdout, *dir, *caseName, *redeliver); err != nil {
		fmt.Fprintf(stderr, "assetcheck: %v\n", err)
		return media.ExitCode(err)
	}
	return 0
}
