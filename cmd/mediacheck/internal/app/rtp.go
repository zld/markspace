package app

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/zld/media-check/transport"
)

func RunRTP(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("rtp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	window := fs.Int("window", 4, "reorder window")
	seed := fs.Int64("seed", 41, "seed")
	var dedupW int
	fs.IntVar(&dedupW, "dedup-window", 64, "duplicate window")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	drill, err := transport.DefaultRTPDrill(*seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rtp: %v\n", err)
		return ExitCode(err)
	}
	stats, gaps, ordered, err := drill.Replay(*window, dedupW, 0x4d454449)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rtp: %v\n", err)
		return ExitCode(err)
	}
	_ = ctx
	fmt.Printf("received=%d duplicates=%d reordered=%d lost=%d dispatched=%d\n",
		stats.Received, stats.Duplicates, stats.Reordered, stats.Lost, len(ordered))
	for _, g := range gaps {
		fmt.Printf("gap start=%d end=%d state=%s\n", g.Start, g.End, g.State)
	}
	return ExitOK
}
