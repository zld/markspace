// Command mediakey is the local media encryption and verification tool.
// Subcommands:
//
//	mediakey demo    - generate deterministic content and show every path
//	mediakey encrypt - encrypt a local file into a JSON manifest
//	mediakey verify  - verify a manifest produced by encrypt
//
// Exit codes: 0 run completed (partial chunk failures allowed), 2 bad flags,
// 3 malformed manifest/input, 4 license or key-dispatch initialization error.
package main

import (
	"fmt"
	"os"

	"mediakey/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mediakey <demo|encrypt|verify> [flags]")
		os.Exit(2)
	}
	code := cli.Run(os.Args[1:])
	os.Exit(code)
}
