package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"streamlab/internal/udp"
)

// runListen binds a loopback UDP socket and runs the shared pipeline
// in real time until the duration elapses or a signal arrives.
func runListen(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	var pf pipeFlags
	pf.register(fs)
	addr := fs.String("addr", "127.0.0.1:9000", "loopback listen address")
	dur := fs.Duration("duration", 10*time.Second, "max run duration")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *dur <= 0 || *dur > 120*time.Second {
		fmt.Fprintln(os.Stderr, "duration must be in (0,120s]")
		return exitUsage
	}
	host, port, err := net.SplitHostPort(*addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsage
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		fmt.Fprintln(os.Stderr, "addr must be a loopback address")
		return exitUsage
	}
	pipe, err := pf.build()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsage
	}
	conn, err := net.ListenPacket("udp", net.JoinHostPort(ip.String(), port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		return exitRuntime
	}
	defer conn.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "listening on %s for %s\n", conn.LocalAddr(), *dur)
	if err := udp.Run(ctx, conn, pipe, *dur); err != nil {
		fmt.Fprintf(os.Stderr, "receive: %v\n", err)
		return exitRuntime
	}
	return emit(out, &pf, pipe)
}
