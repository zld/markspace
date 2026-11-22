package main

import (
	"testing"
	"time"

	"mediacore/internal/clock"
)

func TestParseDemoArgsValidates(t *testing.T) {
	if _, err := parseDemoArgs([]string{"--duration", "0s", "--target-bps", "100",
		"--batch-bytes", "1024", "--fixtures", "x"}); clock.Classify(err) != clock.KindInvalidArgument {
		t.Fatalf("zero duration: %v", err)
	}
	if _, err := parseDemoArgs([]string{"--duration", "10s", "--target-bps", "0",
		"--batch-bytes", "1024", "--fixtures", "x"}); clock.Classify(err) != clock.KindInvalidArgument {
		t.Fatalf("zero bps: %v", err)
	}
	if _, err := parseDemoArgs([]string{"--duration", "10s", "--target-bps", "100",
		"--batch-bytes", "3000000", "--fixtures", "x"}); clock.Classify(err) != clock.KindInvalidArgument {
		t.Fatalf("oversized batch: %v", err)
	}
	if _, err := parseDemoArgs([]string{"--duration", "10s", "--target-bps", "100",
		"--batch-bytes", "1024", "--slow-every", "-1", "--fixtures", "x"});
		clock.Classify(err) != clock.KindInvalidArgument {
		t.Fatalf("negative slow: %v", err)
	}
}

func TestParseDemoArgsDefaultsAndValues(t *testing.T) {
	args, err := parseDemoArgs([]string{"--duration", "60s", "--target-bps", "900000",
		"--batch-bytes", "524288", "--slow-every", "3", "--fixtures", "fx"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Duration != 60*time.Second || args.TargetBPS != 900000 ||
		args.BatchBytes != 524288 || args.SlowEvery != 3 || args.Fixtures != "fx" {
		t.Fatalf("parsed wrong: %+v", args)
	}
}

func TestRunDemoSuccessExitZero(t *testing.T) {
	dir := t.TempDir()
	args := DemoArgs{
		Duration: 20 * time.Second, TargetBPS: 1_800_000,
		BatchBytes: 1 << 20, SlowEvery: 0, Fixtures: dir,
	}
	code, err := runDemo(args)
	if err != nil {
		t.Fatalf("runDemo: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit=%d want 0", code)
	}
}
