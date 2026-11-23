package main

import (
	"flag"
	"fmt"

	"mediatool/internal/fixture"
)

// runScan generates a capture and prints every located fault, ordered by byte
// offset, so an operator can jump straight to the damage.
func runScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	seed := fs.Int64("seed", 42, "fixture seed")
	tracks := fs.Int("tracks", 4, "track count")
	bitrates := fs.Int("bitrates", 3, "representation ladder depth")
	injectLoss := fs.Int("inject-loss", 7, "packets to drop")
	injectTrunc := fs.Int("inject-truncation", 1, "datagrams to truncate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := fixture.DemoOptions(*seed, *tracks, *bitrates, *injectLoss, *injectTrunc)
	cap, err := fixture.Generate(opts)
	if err != nil {
		return err
	}
	scanRes, err := buildIndex(cap, opts)
	if err != nil {
		return err
	}
	fmt.Printf("input_digest=%s\n", scanRes.Index.InputDigest)
	fmt.Printf("input_bytes=%d\n", len(cap.Bytes))
	fmt.Printf("packets=%d srs=%d lost=%d reordered=%d duplicates=%d truncated=%d\n",
		scanRes.Stats.Packets, scanRes.Stats.SRs, scanRes.Stats.Lost,
		scanRes.Stats.Reordered, scanRes.Stats.Duplicates, scanRes.Stats.Truncated)
	for _, f := range scanRes.Index.Faults {
		fmt.Printf("fault kind=%s track=%d offset=%d recovery=%s detail=%q\n",
			f.Kind, f.TrackID, f.Offset, f.Recovery, f.Detail)
	}
	return nil
}
