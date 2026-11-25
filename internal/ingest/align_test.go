package ingest

import (
	"testing"

	"regress-kernel/internal/media"
)

func TestAlignSyncError(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	r := NewRTPReceiver(DefaultRTPConfig())
	for _, p := range fx.Delivered {
		if err := r.Receive(p); err != nil {
			t.Fatalf("receive: %v", err)
		}
	}
	audio, _, err := r.Finalize()
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	clock := fx.Config.Clock
	rep := Align(fx.Renditions[0].Frames, audio, clock)
	if rep.SampleCount != len(fx.Renditions[0].Frames) {
		t.Fatalf("sample count %d want %d", rep.SampleCount, len(fx.Renditions[0].Frames))
	}
	// Preset 5ms offset, 20ms audio grid vs 100ms video grid => <= 10ms error.
	if got := rep.SyncErrorMs(); got > 20 {
		t.Fatalf("sync error %.3fms exceeds 20ms budget", got)
	}
	// Deterministic and exactly reproducible from the same inputs.
	rep2 := Align(fx.Renditions[0].Frames, audio, clock)
	if rep2.MaxAbsErrorNs != rep.MaxAbsErrorNs || rep2.MeanErrorNs != rep.MeanErrorNs {
		t.Fatal("alignment is not deterministic")
	}
	// Every sample is expressed on the shared nanosecond base.
	for _, s := range rep.Samples {
		if s.VideoNanos%int64(1_000_000) != 0 { // video ticks are whole ms
			t.Fatalf("video sample %d not on ms grid: %d", s.FrameIndex, s.VideoNanos)
		}
		if s.ErrorNanos != s.AudioNanos-s.VideoNanos {
			t.Fatalf("sample %d error arithmetic wrong", s.FrameIndex)
		}
	}
}

func TestAlignWithRecoveredPackets(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	r := NewRTPReceiver(DefaultRTPConfig())
	for _, p := range fx.Delivered {
		_ = r.Receive(p)
	}
	audio, rep, err := r.Finalize()
	if err != nil || rep.Recovered == 0 {
		t.Fatalf("fixture must contain recovered packets: %+v err=%v", rep, err)
	}
	// Recovered packets fill the timeline, so every frame still finds a pair.
	sync := Align(fx.Renditions[0].Frames, audio, fx.Config.Clock)
	if sync.SampleCount != len(fx.Renditions[0].Frames) {
		t.Fatalf("recovered audio dropped frame pairs: %d", sync.SampleCount)
	}
}

func TestAlignUnrecoverableStillMeasurable(t *testing.T) {
	fx := media.GenerateUnrecoverable(media.DefaultConfig("normal", 17))
	r := NewRTPReceiver(DefaultRTPConfig())
	for _, p := range fx.Delivered {
		_ = r.Receive(p)
	}
	audio, _, err := r.Finalize()
	if err == nil {
		t.Fatal("burst fixture should surface a loss error")
	}
	// The explicitly marked (not skipped) packets keep alignment measurable.
	sync := Align(fx.Renditions[0].Frames, audio, fx.Config.Clock)
	if sync.SampleCount == 0 {
		t.Fatal("sync statistics must remain available after loss")
	}
}

func TestContinuityError(t *testing.T) {
	fx := media.Generate(media.DefaultConfig("normal", 17))
	clock := fx.Config.Clock
	if gap := ContinuityError(fx.Renditions[0].Frames, clock); gap != 100_000_000 {
		t.Fatalf("clean track gap = %d, want 100ms", gap)
	}
	cfg := media.DefaultConfig("corrupt", 17)
	cfg.Corrupt = media.Corruption{MissingAt: 10}
	bad := media.Generate(cfg)
	if gap := ContinuityError(bad.Renditions[0].Frames, clock); gap != 200_000_000 {
		t.Fatalf("gap after missing frame = %d, want 200ms", gap)
	}
}
