// relaydiag is a loopback WebRTC RTP relay diagnostic tool.
//
// Exit codes: 0 success, 1 negotiation/connection/forward failure,
// 2 parameter error.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"relaydiag/internal/config"
	"relaydiag/internal/runner"
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
	sum, err := runner.Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "relaydiag:", err)
		return 1
	}
	if cfg.JSON {
		enc := json.NewEncoder(os.Stdout)
		if err := enc.Encode(sum); err != nil {
			fmt.Fprintln(os.Stderr, "relaydiag: encode summary:", err)
			return 1
		}
	} else {
		fmt.Printf("candidate_pairs=%d selected_pair_state=%s\n", sum.CandidatePairs, sum.SelectedPairState)
		fmt.Printf("forwarded_packets=%d lost_packets=%d duplicate_packets=%d reordered_packets=%d\n",
			sum.ForwardedPackets, sum.LostPackets, sum.DuplicatePackets, sum.ReorderedPackets)
		fmt.Printf("jitter_ms=%.3f jitter_samples_insufficient=%t duration_ms=%d\n",
			sum.JitterMs, sum.JitterInsufficient, sum.DurationMs)
	}
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
