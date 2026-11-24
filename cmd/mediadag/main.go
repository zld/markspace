// Command mediadag verifies RTP/RTCP live-stream forwarding by building a
// reproducible analysis DAG: ingest -> timeline -> inspect -> manifest ->
// report, with checkpointed resume and categorized failure propagation.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mediadag <command> [flags]\ncommands: demo")
		os.Exit(64)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(64)
	}
}
