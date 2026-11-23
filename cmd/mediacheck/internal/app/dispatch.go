package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, Usage())
		return ExitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunWithContext(ctx, args)
}

// runWithContext is the testable entry point; a cancelled ctx surfaces as exit
// code 130 so shells and supervisors can distinguish interruption.
func RunWithContext(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, Usage())
		return ExitUsage
	}
	switch args[0] {
	case "demo":
		return RunDemo(ctx, args[1:])
	case "verify":
		return RunVerify(ctx, args[1:])
	case "rtp":
		return RunRTP(ctx, args[1:])
	case "-h", "--help", "help":
		fmt.Print(Usage())
		return ExitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s", args[0], Usage())
		return ExitUsage
	}
}

// exitCode maps an error onto the documented exit codes.
