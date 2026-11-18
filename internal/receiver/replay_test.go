package receiver

import (
	"testing"

	"jitterlab/internal/buffer"
	"jitterlab/internal/testkit"
)

func replayConfig() ReplayConfig {
	return ReplayConfig{
		Buffer: buffer.Config{FrameMs: 20, TargetMs: 80, MaxWaitMs: 240, WindowSize: 64},
		Options: testkit.Options{
			Frames: 300, FrameMs: 20, Seed: 41,
			Loss: 0.04, Reorder: 0.08, Duplicate: 0.02,
			ReorderDelay: 3, DuplicateDelay: 4,
		},
		Decay:         0.6,
		MaxInvalidRun: 16,
	}
}

func TestReplayIsDeterministic(t *testing.T) {
	a, err := Replay(replayConfig())
	if err != nil {
		t.Fatalf("first replay: %v", err)
	}
	b, err := Replay(replayConfig())
	if err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if len(a.Frames) != len(b.Frames) {
		t.Fatalf("frame counts differ: %d vs %d", len(a.Frames), len(b.Frames))
	}
	for i := range a.Frames {
		if a.Frames[i] != b.Frames[i] {
			t.Fatalf("frame %d differs: %+v vs %+v", i, a.Frames[i], b.Frames[i])
		}
	}
	if a.Summary != b.Summary {
		t.Fatalf("summary differs:\n%+v\n%+v", a.Summary, b.Summary)
	}
}

func TestReplayProducesFramesAndImpairments(t *testing.T) {
	res, err := Replay(replayConfig())
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.ExitCode != ExitOK {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if len(res.Frames) == 0 {
		t.Fatal("no frames played")
	}
	s := res.Summary
	// Input can be reconciled with the plan: every frame is either lost
	// before transmission or received once, and duplicates add datagrams.
	if s.InputPackets != 300-s.LostPackets+s.InjectedDuplicates {
		t.Fatalf("input = %d, want frames-lost+dups = %d",
			s.InputPackets, 300-s.LostPackets+s.InjectedDuplicates)
	}
	if s.DroppedPackets == 0 {
		t.Fatalf("summary = %+v, want drops under loss", s)
	}
	if s.ConcealedFrames == 0 || s.MaxConcealRun == 0 {
		t.Fatalf("summary = %+v, want concealment", s)
	}
	if s.BufferMsMin < 0 || s.BufferMsMax > 240 {
		t.Fatalf("buffer range %d..%d outside max wait", s.BufferMsMin, s.BufferMsMax)
	}
	kinds := map[string]int{}
	for _, f := range res.Frames {
		kinds[f.Kind]++
	}
	if kinds["real"] == 0 || kinds["extend"] == 0 {
		t.Fatalf("kinds = %v, want real and concealed slots", kinds)
	}
}

func TestReplayDifferentSeedDiffers(t *testing.T) {
	cfg := replayConfig()
	a, _ := Replay(cfg)
	cfg.Options.Seed = 42
	b, _ := Replay(cfg)
	if a.Summary.DroppedPackets == b.Summary.DroppedPackets &&
		a.Summary.ConcealedFrames == b.Summary.ConcealedFrames &&
		len(a.Frames) == len(b.Frames) {
		t.Fatalf("seeds 41 and 42 produced the same outcome: %+v", a.Summary)
	}
}

func TestReplayBurstLossRaisesConcealRun(t *testing.T) {
	cfg := replayConfig()
	cfg.Options.Loss = 0
	cfg.Options.Reorder = 0
	cfg.Options.Duplicate = 0
	base, err := Replay(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Options.BurstStart = 0.02
	cfg.Options.BurstLen = 6
	bursty, err := Replay(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bursty.Summary.MaxConcealRun <= base.Summary.MaxConcealRun {
		t.Fatalf("burst run %d not above clean run %d",
			bursty.Summary.MaxConcealRun, base.Summary.MaxConcealRun)
	}
}

func TestReplayDriftChangesTiming(t *testing.T) {
	cfg := replayConfig()
	cfg.Options.Reorder = 0
	cfg.Options.Duplicate = 0
	base, _ := Replay(cfg)
	// A positive drift stretches arrival intervals on the receiver clock,
	// so playout must conceal more often than with a matched clock.
	cfg.Options.DriftPPM = 50_000
	drift, err := Replay(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if drift.Summary.Frames <= base.Summary.Frames {
		t.Fatalf("drifted frames = %d, want more than %d",
			drift.Summary.Frames, base.Summary.Frames)
	}
	if drift.Summary.ConcealedFrames <= base.Summary.ConcealedFrames {
		t.Fatalf("drifted concealed = %d, want more than %d",
			drift.Summary.ConcealedFrames, base.Summary.ConcealedFrames)
	}
}

func TestReplayDamagedStreamExitsThree(t *testing.T) {
	cfg := replayConfig()
	cfg.Options = testkit.Options{Frames: 100, FrameMs: 20, Seed: 5, Corrupt: 1}
	res, err := Replay(cfg)
	if err == nil {
		t.Fatal("expected damage error")
	}
	if res.ExitCode != ExitDamaged {
		t.Fatalf("exit = %d, want %d", res.ExitCode, ExitDamaged)
	}
	if res.Summary.LongestBadRun < cfg.MaxInvalidRun {
		t.Fatalf("longest bad run = %d, want >= %d", res.Summary.LongestBadRun, cfg.MaxInvalidRun)
	}
}

func TestReplayInvalidConfig(t *testing.T) {
	cfg := replayConfig()
	cfg.Buffer.TargetMs = 400
	cfg.Buffer.MaxWaitMs = 200
	if res, err := Replay(cfg); err == nil || res.ExitCode != ExitUsage {
		t.Fatalf("got exit %d err %v, want usage error", res.ExitCode, err)
	}
}
