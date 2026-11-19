// Package cli parses command-line flags and maps pipeline errors onto the
// documented process exit codes.
package cli

import (
	"fmt"
	"os"

	"mediakey/internal/manifest"
)

// Exit codes are part of the tool's scripting contract.
const (
	ExitOK            = 0
	ExitUsage         = 2
	ExitBadManifest   = 3
	ExitInitFailure   = 4
)

// Run dispatches a subcommand and returns the process exit code.
func Run(args []string) int {
	if len(args) == 0 {
		usage()
		return ExitUsage
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "encrypt":
		return runEncrypt(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		usage()
		return ExitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		usage()
		return ExitUsage
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediakey <demo|encrypt|verify> [flags]")
	fmt.Fprintln(os.Stderr, "  demo    -chunks N -chunk-size B -rotate-every N [-inject a,b,c] [-workers N]")
	fmt.Fprintln(os.Stderr, "  encrypt -input FILE -output FILE -chunk-size B -rotate-every N [-asset ID]")
	fmt.Fprintln(os.Stderr, "  verify  -manifest FILE [-workers N]")
}

// fail maps a pipeline error to an exit code and prints a category-only
// message; key material and plaintext are never written anywhere.
func fail(err error) int {
	code := manifest.ErrorCode(err)
	switch code {
	case manifest.ReasonInvalidInput, manifest.ReasonLimitExceeded:
		fmt.Fprintf(os.Stderr, "parameter error: %s\n", code)
		return ExitUsage
	case manifest.ReasonLicenseSignature, manifest.ReasonAssetMismatch,
		manifest.ReasonRangeExceeded, manifest.ReasonNotYetValid,
		manifest.ReasonExpired, manifest.ReasonKeyExpired,
		manifest.ReasonUnknownRound, manifest.ReasonDispatcherClosed,
		manifest.ReasonRevokedRound:
		fmt.Fprintf(os.Stderr, "license/key init error: %s\n", code)
		return ExitInitFailure
	default:
		fmt.Fprintf(os.Stderr, "manifest/input error: %s\n", code)
		return ExitBadManifest
	}
}
