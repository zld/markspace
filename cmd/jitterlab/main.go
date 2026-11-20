// Command jitterlab drives reproducible jitter-buffer experiments over a
// synthetic trace or a loopback UDP socket.
package main

import (
	"fmt"
	"io"
	"os"
)

// Exit codes are part of the command contract.
const (
	exitOK      = 0
	exitUsage   = 2
	exitInput   = 3
	exitRuntime = 4
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: jitterlab <simulate|listen|send> [flags]")
	fmt.Fprintln(w, "  simulate  generate a trace and run the deterministic receiver")
	fmt.Fprintln(w, "  listen    receive UDP datagrams on a loopback address")
	fmt.Fprintln(w, "  send      replay a synthetic trace to a UDP address (verification aid)")
	fmt.Fprintln(w, "exit codes: 0 ok, 2 argument error, 3 input/packet error, 4 network/runtime error")
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "simulate":
		return runSimulate(args[1:], stdout, stderr)
	case "listen":
		return runListen(args[1:], stdout, stderr)
	case "send":
		return runSend(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return exitUsage
	}
}
