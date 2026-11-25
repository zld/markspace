// Command mediacore is the terminal acceptance adapter.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"mediacore/internal/selftest"
	"mediacore/internal/verify"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: mediacore <selftest|verify-manifest> [flags]")
		return 2
	}
	switch args[0] {
	case "selftest":
		return runSelftest(args[1:], stdout, stderr)
	case "verify-manifest":
		return runVerifyManifest(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "mediacore selftest --generate --work ./tmp --report text")
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func runSelftest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("selftest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	generate := fs.Bool("generate", false, "generate deterministic fixtures")
	work := fs.String("work", "./tmp", "working directory")
	report := fs.String("report", "text", "report format: text or json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*generate {
		fmt.Fprintln(stderr, "selftest currently requires --generate")
		return 2
	}
	rep, err := selftest.Run(*work)
	if *report == "json" {
		data, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprint(stdout, selftest.FormatText(rep))
	}
	if err != nil {
		fmt.Fprintf(stderr, "selftest failed: %v\n", err)
		return 1
	}
	return 0
}

func runVerifyManifest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify-manifest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("manifest", "", "path to manifest JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(stderr, "--manifest is required")
		return 2
	}
	m, err := verify.VerifyManifestArtifacts(*path)
	if err != nil {
		fmt.Fprintf(stderr, "verification failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "manifest_ok case=%s input=%s output=%s alignment_ms=%.4f\n",
		m.Case, m.Input.SHA256, m.Output.SHA256, m.Metrics.ManifestAlignmentMS)
	return 0
}
