package receiver

import (
	"context"
	"runtime"
	"testing"
	"time"

	"jitterlab/internal/buffer"
	"jitterlab/internal/netem"
	"jitterlab/internal/testkit"
)

func liveConfig() LiveConfig {
	return LiveConfig{
		Buffer: buffer.Config{FrameMs: 10, TargetMs: 40, MaxWaitMs: 120, WindowSize: 64},
		Options: testkit.Options{
			Frames: 60, FrameMs: 10, Seed: 11,
			Loss: 0.05, Reorder: 0.05, Duplicate: 0.02,
			ReorderDelay: 2, DuplicateDelay: 2,
		},
		Decay:         0.6,
		MaxInvalidRun: 12,
		Timeout:       50 * time.Millisecond,
	}
}

func TestLiveRunsOverLoopbackAndReleases(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	before := runtime.NumGoroutine()

	cfg := liveConfig()
	res, err := Live(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if res.ExitCode != ExitOK {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if res.Port == 0 {
		t.Fatal("no port reported")
	}
	if len(res.Frames) == 0 {
		t.Fatal("no frames played")
	}
	if res.Sent == 0 {
		t.Fatal("sender transmitted nothing")
	}
	if res.Sent+res.Lost-res.Duplicated != cfg.Options.Frames {
		t.Fatalf("sent %d - dups %d + lost %d != frames %d",
			res.Sent, res.Duplicated, res.Lost, cfg.Options.Frames)
	}

	// Sockets must be closed: the ephemeral port is free to bind again.
	if rx, err := netem.Listen(res.Port); err != nil {
		t.Fatalf("port %d not released: %v", res.Port, err)
	} else {
		rx.Close()
	}
	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Fatalf("goroutines before %d after %d, likely leaked", before, after)
	}
}

func TestLiveStopsOnContextCancel(t *testing.T) {
	cfg := liveConfig()
	cfg.Options.Frames = testkit.MaxFrames
	cfg.Options.Loss = 0
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, _ := Live(ctx, cfg)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Live ignored cancellation for %v", elapsed)
	}
	_ = res
}

func TestLiveDetectsSustainedCorruption(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	cfg := liveConfig()
	cfg.Options.Corrupt = 1 // every datagram has a bad checksum
	cfg.MaxInvalidRun = 5
	res, err := Live(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected damage error")
	}
	if res.ExitCode != ExitDamaged {
		t.Fatalf("exit = %d, want %d", res.ExitCode, ExitDamaged)
	}
}

func TestLiveInvalidConfig(t *testing.T) {
	cfg := liveConfig()
	cfg.Buffer.WindowSize = -1
	if res, err := Live(context.Background(), cfg); err == nil || res.ExitCode != ExitUsage {
		t.Fatalf("exit = %d err = %v", res.ExitCode, err)
	}
}
