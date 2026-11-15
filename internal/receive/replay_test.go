package receive

import (
	"testing"

	"jbbench/internal/jitter"
	"jbbench/internal/sim"
	"jbbench/internal/testkit"
)

func TestReplayClassifiesEveryImpairment(t *testing.T) {
	cfg := sim.DefaultConfig()
	cfg.Frames = 2500
	cfg.Seed = 11
	cfg.LossRate = 0.05
	cfg.LossBurst = 6
	cfg.DuplicateRate = 0.04
	cfg.CorruptRate = 0.02
	cfg.ReorderWindow = 10
	events, err := sim.Generate(cfg)
	testkit.Require(t, err, "generate")
	testkit.AssertScenarioAccounting(t, events, cfg.Frames)

	rep, err := Replay(events, cfg.Frames, jitter.DefaultPlayerConfig())
	testkit.Require(t, err, "replay")
	testkit.AssertReportFields(t, rep)

	if rep.Frames != uint64(cfg.Frames) {
		t.Fatalf("frames = %d, want %d", rep.Frames, cfg.Frames)
	}
	if rep.Received == 0 {
		t.Fatal("no valid packets received")
	}
	if rep.Corrupt == 0 || rep.BadVersion == 0 || rep.BadLength == 0 {
		t.Errorf("bad-packet counters missing: %+v", rep.DropCounters)
	}
	if rep.Ambiguous == 0 {
		t.Error("ambiguity fixture not counted")
	}
	if rep.Duplicate == 0 {
		t.Error("duplicate packets not counted")
	}
	if rep.ConcealedFrames == 0 {
		t.Error("loss bursts produced no concealed frames")
	}
	if rep.MaxConcealRun == 0 {
		t.Error("max conceal run not recorded")
	}
}

func TestReplayCleanStream(t *testing.T) {
	cfg := sim.DefaultConfig()
	cfg.Frames = 400
	cfg.LossRate = 0
	cfg.DuplicateRate = 0
	cfg.CorruptRate = 0
	cfg.ReorderWindow = 0
	cfg.JitterNS = 0
	events, err := sim.Generate(cfg)
	testkit.Require(t, err, "generate")

	rep, err := Replay(events, cfg.Frames, jitter.DefaultPlayerConfig())
	testkit.Require(t, err, "replay")
	// The only invalid item is the deterministic half-interval ambiguity
	// fixture, which sits outside the configured frame range.
	if rep.Ambiguous != 1 || rep.Corrupt != 0 || rep.BadVersion != 0 ||
		rep.BadLength != 0 || rep.Duplicate != 0 ||
		rep.Late != 0 || rep.Overflow != 0 || rep.ConcealedFrames != 0 {
		t.Fatalf("clean stream produced unexpected drops/concealment: %+v", rep)
	}
	if rep.Received != uint64(cfg.Frames) {
		t.Fatalf("received = %d, want %d", rep.Received, cfg.Frames)
	}
	if rep.Played != uint64(cfg.Frames) {
		t.Fatalf("played = %d, want %d", rep.Played, cfg.Frames)
	}
	if rep.MaxConcealRun != 0 || rep.RecoveryRatio != 0 {
		t.Fatalf("clean stream concealment stats wrong: %+v", rep)
	}
}

func TestReplayDeterministic(t *testing.T) {
	cfg := sim.DefaultConfig()
	cfg.Frames = 800
	events, err := sim.Generate(cfg)
	testkit.Require(t, err, "generate")
	a, err := Replay(events, cfg.Frames, jitter.DefaultPlayerConfig())
	testkit.Require(t, err, "replay 1")
	b, err := Replay(events, cfg.Frames, jitter.DefaultPlayerConfig())
	testkit.Require(t, err, "replay 2")
	if a.Received != b.Received || a.Played != b.Played ||
		a.ConcealedFrames != b.ConcealedFrames ||
		a.MaxConcealRun != b.MaxConcealRun ||
		a.DropCounters != b.DropCounters ||
		len(a.BufferLevelCurve) != len(b.BufferLevelCurve) {
		t.Fatal("replay is not deterministic across runs")
	}
}
