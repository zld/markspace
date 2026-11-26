// Command replayctl is the terminal acceptance entry point.
package main

import (
	"os"

	"replayctl/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
