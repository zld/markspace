package tests

import (
	"testing"
	"time"

	"jitterlab/internal/jitter"
	"jitterlab/internal/metrics"
	"jitterlab/internal/packet"
)

func TestAdaptiveTargetRisesAndFalls(t *testing.T) {
	cfg := jitter.DefaultConfig()
	cfg.MaxTargetMS = 250
	ctrl := jitter.NewController(cfg, metrics.New())
	frame := 20 * time.Millisecond
	ts := uint32(500_000)
	seq := 0
	send := func(arrival time.Duration) {
		wire := mustPacket(t, seq, ts+uint32(seq*20), byte(seq))
		ctrl.AddPacket(wire, arrival)
		ctrl.AdvanceTo(arrival)
		seq++
	}
	// 200 frames of alternating 80 ms delay spikes.
	for i := 0; i < 200; i++ {
		at := time.Duration(seq+1)*frame + 10*time.Millisecond
		if i%2 == 0 {
			at += 80 * time.Millisecond
		}
		send(at)
	}
	spiky, ok := ctrl.TargetMS(1)
	if !ok {
		t.Fatal("stream not tracked")
	}
	// 6 s of stable arrivals: the target must decay gradually.
	for i := 0; i < 300; i++ {
		send(time.Duration(seq+1)*frame + 10*time.Millisecond)
	}
	stable, _ := ctrl.TargetMS(1)
	if spiky <= cfg.InitialTargetMS {
		t.Fatalf("target did not rise under jitter: %.2f", spiky)
	}
	if stable >= spiky {
		t.Fatalf("target did not settle: spiky=%.2f stable=%.2f", spiky, stable)
	}
	if stable < cfg.MinTargetMS || stable > cfg.MaxTargetMS {
		t.Fatalf("target %.2f outside [%.0f,%.0f]", stable, cfg.MinTargetMS, cfg.MaxTargetMS)
	}
	steps := []float64{}
	prev := stable
	for i := 0; i < 20; i++ {
		send(time.Duration(seq+1)*frame + 10*time.Millisecond)
		cur, _ := ctrl.TargetMS(1)
		steps = append(steps, prev-cur)
		prev = cur
	}
	for i, s := range steps {
		if s > 1.0 {
			t.Fatalf("target fell by %.2f at step %d; decay must be gradual", s, i)
		}
	}
}

func TestConcealmentRunsAndRecovery(t *testing.T) {
	cfg := jitter.DefaultConfig()
	cfg.HiddenThreshold = 3
	ctrl, _ := newController(t, cfg)
	ts := uint32(900_000)
	seq := 0
	for ; seq < 10; seq++ {
		at := time.Duration(seq+1) * 20 * time.Millisecond
		ctrl.AddPacket(mustPacket(t, seq, ts+uint32(seq*20), byte(seq)), at)
		ctrl.AdvanceTo(at)
	}
	// Drop seq 10..17: eight consecutive missing frames.
	for seq = 18; seq < 30; seq++ {
		at := time.Duration(seq+1) * 20 * time.Millisecond
		ctrl.AddPacket(mustPacket(t, seq, ts+uint32(seq*20), byte(seq)), at)
		ctrl.AdvanceTo(at)
	}
	// Words 30..34 missing again, then recovery must reset the run.
	for seq = 35; seq < 45; seq++ {
		at := time.Duration(seq+1) * 20 * time.Millisecond
		ctrl.AddPacket(mustPacket(t, seq, ts+uint32(seq*20), byte(seq)), at)
		ctrl.AdvanceTo(at)
	}
	ctrl.CloseInput()
	ctrl.DrainTail(time.Second)
	ctrl.Finalize()
	s := ctrl.Summary()
	if s.HiddenFrames < 8 {
		t.Fatalf("hidden frames = %d, want >= 8", s.HiddenFrames)
	}
	if s.MaxHiddenRun != 8 {
		t.Fatalf("max hidden run = %d, want 8", s.MaxHiddenRun)
	}
	if s.HiddenDegrades != 2 {
		t.Fatalf("degradations = %d, want 2", s.HiddenDegrades)
	}
	if s.FramesPlayed != 32 {
		t.Fatalf("real frames = %d, want 32", s.FramesPlayed)
	}
	if ratio := s.ContinuityRatio; ratio <= 0 || ratio >= 1 {
		t.Fatalf("continuity %v out of (0,1)", ratio)
	}
}

func TestTimestampRegressionAndInterruption(t *testing.T) {
	cfg := jitter.DefaultConfig()
	ctrl, _ := newController(t, cfg)
	baseTS := uint32(2_000_000)
	// Frames 0..4 at 20 ms spacing.
	for seq := 0; seq < 5; seq++ {
		at := time.Duration(seq+1) * 20 * time.Millisecond
		ctrl.AddPacket(mustPacket(t, seq, baseTS+uint32(seq*20), byte(seq)), at)
		ctrl.AdvanceTo(at)
	}
	// Frame 5 arrives with a capture timestamp 30 s in the past while the
	// play cursor is far behind: explicit regression.
	ctrl.AddPacket(mustPacket(t, 5, baseTS-30_000, 5), 120*time.Millisecond)
	// A long absence followed by frame 6 at 5 s: stream interruption.
	ctrl.AddPacket(mustPacket(t, 6, baseTS+uint32(6*20), 6), 5*time.Second)
	ctrl.CloseInput()
	ctrl.DrainTail(2 * time.Second)
	ctrl.Finalize()
	s := ctrl.Summary()
	if s.DropTimestampRegression != 1 {
		t.Fatalf("regression drops = %d, want 1", s.DropTimestampRegression)
	}
	if s.StreamInterruptions != 1 {
		t.Fatalf("interruptions = %d, want 1", s.StreamInterruptions)
	}
}

func TestMultiFramePacketCounts(t *testing.T) {
	cfg := jitter.DefaultConfig()
	ctrl, _ := newController(t, cfg)
	p, err := packet.New(3, 0, 5000, 4, false, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := packet.Marshal(p)
	if !ctrl.AddPacket(wire, 0) {
		t.Fatal("multi-frame packet rejected")
	}
	ctrl.AdvanceTo(time.Second)
	ctrl.CloseInput()
	ctrl.DrainTail(time.Second)
	ctrl.Finalize()
	s := ctrl.Summary()
	if s.FramesPlayed != 4 {
		t.Fatalf("frames played = %d, want 4", s.FramesPlayed)
	}
}

func TestInvalidPacketsNeverReachBuffer(t *testing.T) {
	cfg := jitter.DefaultConfig()
	ctrl, _ := newController(t, cfg)
	good := mustPacket(t, 0, 1000, 1)

	bad := map[string][]byte{
		"truncated":    good[:len(good)-1],
		"short header": good[:3],
		"bad version":  mutate(good, func(b []byte) { b[0] = 99 }),
		"zero frames":  mutate(good, func(b []byte) { b[12] = 0 }),
		"huge frames":  mutate(good, func(b []byte) { b[12] = 200 }),
		"bad crc":      mutate(good, func(b []byte) { b[15] ^= 0xff }),
		"overlong":     append(append([]byte(nil), good...), 0),
	}
	ctrl.AddPacket(good, 0)
	before := ctrl.QueueStats()
	for name, data := range bad {
		if ctrl.AddPacket(data, time.Millisecond) {
			t.Fatalf("%s: invalid packet was accepted", name)
		}
	}
	after := ctrl.QueueStats()
	if after.Buffered != before.Buffered || after.Accepted != before.Accepted {
		t.Fatalf("buffer changed after invalid input: %+v -> %+v", before, after)
	}
	ctrl.Finalize()
	if got := ctrl.Summary().PacketsInvalid; got != int64(len(bad)) {
		t.Fatalf("invalid count = %d, want %d", got, len(bad))
	}
}
