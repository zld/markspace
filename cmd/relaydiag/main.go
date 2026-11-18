// relaydiag is a loopback WebRTC RTP relay diagnostic tool.
//
// Exit codes: 0 success, 1 negotiation/connection/forward failure,
// 2 parameter error.
package main

import (
	"fmt"
	"os"

	"relaydiag/internal/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] != "run" {
		usage()
		return 2
	}
	cfg, err := config.Parse(args[1:], os.Stderr)
	if err != nil {
		return 2
	}
	// Runner wiring lands in a later batch.
	fmt.Fprintf(os.Stderr, "relaydiag: parsed config: %+v\n", cfg)
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, `relaydiag - loopback WebRTC RTP relay diagnostics

Usage:
  relaydiag run [--address 127.0.0.1] [--packets N] [--interval D] [--timeout D] [--json]

Exit codes:
  0  connection and statistics completed
  1  negotiation, connection or forwarding failure
  2  parameter error`)
}
