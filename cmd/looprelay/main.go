// Command looprelay runs a self-contained loopback WebRTC verification.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"looprelay/internal/cli"
	"looprelay/internal/integration"
	"looprelay/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	opts, err := cli.Parse(args)
	if err != nil {
		if errors.Is(err, cli.ErrHelp) {
			fmt.Fprint(os.Stdout, cli.UsageText)
			return integration.ExitOK
		}
		fmt.Fprintf(os.Stderr, "looprelay: %v\n\n%s", err, cli.UsageText)
		return integration.ExitInvalidArgs
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	result := integration.Run(ctx, integration.FromCLI(opts))

	if opts.JSON {
		if err := report.WriteJSON(os.Stdout, result.Report); err != nil {
			fmt.Fprintf(os.Stderr, "looprelay: write report: %v\n", err)
			return integration.ExitIntegrity
		}
	} else {
		printText(result)
	}

	if result.Err != nil && !opts.JSON {
		fmt.Fprintf(os.Stderr, "looprelay: %v\n", result.Err)
	}
	if ctx.Err() != nil {
		return integration.ExitInterrupted
	}
	return result.Exit
}

func printText(result integration.Result) {
	rep := result.Report
	fmt.Printf("status:                %s\n", rep.Status)
	fmt.Printf("candidate pairs:       %d\n", rep.CandidatePairCount)
	for _, link := range rep.Links {
		fmt.Printf("  %-8s %s:%d -> %s:%d (connected=%t)\n", link.Name, link.LocalAddress, link.LocalPort, link.RemoteAddress, link.RemotePort, link.Connected)
	}
	fmt.Printf("generated packets:     %d\n", rep.GeneratedPackets)
	fmt.Printf("skipped packets:       %d\n", rep.SkippedPackets)
	fmt.Printf("forwarded packets:     %d\n", rep.ForwardedPackets)
	fmt.Printf("received packets:      %d\n", rep.ReceivedPackets)
	fmt.Printf("lost packets:          %d\n", rep.LostPackets)
	fmt.Printf("duplicate packets:     %d\n", rep.DuplicatePackets)
	fmt.Printf("queue drops:           %d\n", rep.QueueDrops)
	fmt.Printf("jitter ms (%s): mean=%.3f p95=%.3f max=%.3f\n", rep.JitterMS.Status, rep.JitterMS.Mean, rep.JitterMS.P95, rep.JitterMS.Max)
	if rep.Error != "" {
		fmt.Printf("error:                 %s\n", rep.Error)
	}
}
