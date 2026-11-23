package app

import (
	"context"
	"fmt"
	"os"

	"github.com/zld/media-check/media"
)

func RunDemo(ctx context.Context, args []string) int {
	d, err := parseDemo(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n%s", err, Usage())
		return ExitUsage
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "demo: cancelled")
		return ExitCancelled
	}
	rep, err := demo(ctx, d)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		if d.verbose {
			if ce, ok := media.AsError(err); ok {
				fmt.Fprintf(os.Stderr, "  class=%s offset=%d track=%d segment=%d\n", ce.Class, ce.Offset, ce.TrackID, ce.Segment)
			}
		}
		return ExitCode(err)
	}
	printDemoReport(rep, d.verbose)
	// A signal that arrived while the run was completing still means the
	// operator interrupted us; report that rather than claiming success.
	if ctx.Err() != nil {
		return ExitCancelled
	}
	return ExitOK
}
