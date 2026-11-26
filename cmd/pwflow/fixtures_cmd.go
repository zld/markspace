package main

import (
	"context"
	"flag"
	"fmt"

	"pwflow/internal/fixture"
)

func runFixtures(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("fixtures", flag.ContinueOnError)
	dir := fs.String("dir", "", "output fixture directory")
	seed := fs.Int64("seed", 23, "deterministic generation seed")
	if err := fs.Parse(args); err != nil {
		return &cliError{code: exitRejected, err: err}
	}
	if *dir == "" {
		return &cliError{code: exitRejected, err: fmt.Errorf("--dir is required")}
	}
	paths, err := fixture.Generate(fixture.Options{Dir: *dir, Seed: *seed})
	if err != nil {
		return &cliError{code: exitRejected, err: err}
	}
	fmt.Printf("fixtures generated (seed=%d):\n", *seed)
	fmt.Printf("  standard media : %s\n", paths.StandardWebM)
	fmt.Printf("  damaged media  : %s\n", paths.DamagedWebM)
	fmt.Printf("  events         : %s\n", paths.Events)
	fmt.Printf("  RTP traces     : %s , %s\n", paths.StandardRTP, paths.BurstRTP)
	fmt.Printf("  MP4 reference  : %s\n", paths.ReferenceMP4)
	fmt.Printf("  MP4 timeline   : %s\n", paths.MP4Timeline)
	return nil
}
