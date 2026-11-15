// hlsforge builds and verifies low-latency HLS manifests from local CMAF
// fragments delivered out of order by concurrent recording nodes.
//
// Exit codes:
//
//	0 - success
//	1 - parameter or file error
//	2 - content consistency failure reported by verify
package main

import (
	"fmt"
	"os"

	"hlsforge/internal/cli"
	"hlsforge/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "demo":
		cfg, err := cli.ParseDemo(rest)
		if err != nil {
			return failArgs(err)
		}
		rep, err := runDemo(cfg)
		return finish(cfg.JSON, rep, err)
	case "build":
		cfg, err := cli.ParseBuild(rest)
		if err != nil {
			return failArgs(err)
		}
		rep, err := runBuild(cfg)
		return finish(cfg.JSON, rep, err)
	case "verify":
		cfg, err := cli.ParseVerify(rest)
		if err != nil {
			return failArgs(err)
		}
		rep, err := runVerify(cfg)
		return finish(cfg.JSON, rep, err)
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", cmd)
		usage()
		return 1
	}
}

// finish prints the report and maps it onto the process exit code.
func finish(asJSON bool, rep *report.Report, err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if asJSON {
		if err := report.WriteJSON(os.Stdout, rep); err != nil {
			return 1
		}
	} else {
		report.WriteText(os.Stdout, rep)
	}
	if rep.HasConsistencyFailure() {
		return 2
	}
	return 0
}

func failArgs(err error) int {
	fmt.Fprintln(os.Stderr, "error:", err)
	return 1
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hlsforge <demo|build|verify> [flags]")
}
