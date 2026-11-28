// Package main implements the rtp-rebuild acceptance CLI.
//
// The command generates deterministic fixtures, rebuilds the media timeline in
// stages, validates the artifacts and prints one line of results per item.
//
// Exit codes:
//
//	0  every threshold passed
//	1  a business threshold failed
//	2  unrecoverable parameter or IO error
//	3  a partial track was produced with locatable corruption
package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rtp-rebuild: %v\n%s", err, usage)
		return exitParam
	}
	if cfg.help {
		fmt.Print(usage)
		return exitOK
	}
	if err := cfg.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "rtp-rebuild: %v\n%s", err, usage)
		return exitParam
	}

	report, err := execute(context.Background(), cfg)
	if err != nil {
		if report != nil {
			report.print(os.Stdout)
		}
		fmt.Fprintf(os.Stderr, "rtp-rebuild: %v\n", err)
		return classify(err, report)
	}
	report.print(os.Stdout)
	return report.exitCode()
}
