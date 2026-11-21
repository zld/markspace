// Command mediaseal is a local batch tool that verifies content encryption,
// key rotation and local license validation over generated media segments.
package main

import (
	"os"

	"mediaseal/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args, os.Stdout, os.Stderr))
}
