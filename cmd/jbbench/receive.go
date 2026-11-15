package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"jbbench/internal/metrics"
	"jbbench/internal/receive"
	"jbbench/internal/transport"
)

func runReceive(args []string) int {
	fs := flag.NewFlagSet("receive", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	addr := fs.String("addr", "127.0.0.1:0",
		"loopback UDP address to listen on")
	timeoutMS := fs.Int("read-timeout-ms", 50,
		"idle read deadline in milliseconds")
	maxIdleMS := fs.Int("max-idle-ms", 2000,
		"stop after this many milliseconds without a packet")
	asJSON := fs.Bool("json", false, "emit the full report as JSON")
	if err := fs.Parse(args); err != nil {
		return exitParamError
	}
	if *timeoutMS <= 0 || *maxIdleMS <= 0 {
		fmt.Fprintln(os.Stderr, "jbbench: timeout flags must be positive")
		return exitParamError
	}

	listener, err := transport.NewListener(*addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jbbench: cannot bind %s: %v\n", *addr, err)
		return exitInputError
	}
	fmt.Fprintf(os.Stderr, "jbbench: listening on %s (idle stop after %dms)\n",
		listener.LocalAddr().String(), *maxIdleMS)

	cfg := receive.DefaultLiveConfig()
	cfg.ReadTimeout = time.Duration(*timeoutMS) * time.Millisecond
	cfg.MaxIdle = time.Duration(*maxIdleMS) * time.Millisecond

	rep, err := receive.RunLive(listener, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jbbench: transport error: %v\n", err)
		return exitInputError
	}
	if err := metrics.Validate(rep); err != nil {
		fmt.Fprintf(os.Stderr, "jbbench: internal consistency failure: %v\n", err)
		return exitConsistency
	}
	if *asJSON {
		if err := metrics.WriteJSON(os.Stdout, rep); err != nil {
			return exitInputError
		}
	} else if err := metrics.WriteText(os.Stdout, rep); err != nil {
		return exitInputError
	}
	return exitOK
}
