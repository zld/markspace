package main

import (
	"flag"
	"time"

	"mediacore/internal/clock"
)

func parseDemoArgs(argv []string) (DemoArgs, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var (
		duration   = fs.Duration("duration", 3600*time.Second, "manifest timeline span")
		targetBPS  = fs.Uint64("target-bps", 1_800_000, "target bitrate bits/s")
		batchBytes = fs.Int64("batch-bytes", 1<<20, "per-batch physical byte cap")
		slowEvery  = fs.Int("slow-every", 7, "slow consumer stall period (0=off)")
		fixtures   = fs.String("fixtures", "generated", "fixture output directory")
	)
	if err := fs.Parse(argv); err != nil {
		return DemoArgs{}, clock.ErrInvalidArgument("%v", err)
	}
	if *duration <= 0 {
		return DemoArgs{}, clock.ErrInvalidArgument("duration must be positive")
	}
	if *targetBPS == 0 {
		return DemoArgs{}, clock.ErrInvalidArgument("target-bps must be non-zero")
	}
	if *batchBytes <= 0 || *batchBytes > 2<<20 {
		return DemoArgs{}, clock.ErrInvalidArgument("batch-bytes must be within (0, 2MiB]")
	}
	if *slowEvery < 0 {
		return DemoArgs{}, clock.ErrInvalidArgument("slow-every must be >= 0")
	}
	if *fixtures == "" {
		return DemoArgs{}, clock.ErrInvalidArgument("fixtures directory required")
	}
	return DemoArgs{
		Duration:   *duration,
		TargetBPS:  *targetBPS,
		BatchBytes: *batchBytes,
		SlowEvery:  *slowEvery,
		Fixtures:   *fixtures,
	}, nil
}
