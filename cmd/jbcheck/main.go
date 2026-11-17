// Command jbcheck checks receive-side continuity of a 48 kHz mono PCM16
// audio stream under reproducible network impairments, either by replaying
// a generated trace or by pacing it over loopback UDP.
package main

import (
	"os"

	"jbcheck/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
