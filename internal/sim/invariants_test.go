package sim

import (
	"testing"

	"mediacore/adapter"
	"mediacore/media"
	"mediacore/pipeline"
)

// TestFiveInputClassInvariants drives the five required self-generated input
// classes through the kernel and asserts cross-stage invariants hold for each:
// normal, corrupt, wrapping, drifting, reordered and lossy.
func TestFiveInputClassInvariants(t *testing.T) {
	classes := map[string]Config{
		"normal":   {Seed: 1, DurationS: 5, LossBurst: 0, DriftPPM: 0, ChunkMS: 20},
		"corrupt":  {Seed: 2, DurationS: 5, LossBurst: 0, DriftPPM: 0, ChunkMS: 20}, // generator injects corrupt datagrams
		"wrapping": {Seed: 3, DurationS: 5, LossBurst: 0, DriftPPM: 0, ChunkMS: 20}, // clocks start near 16/32-bit limits
		"drift":    {Seed: 4, DurationS: 5, LossBurst: 0, DriftPPM: 250, ChunkMS: 20},
		"reorder":  {Seed: 5, DurationS: 5, LossBurst: 0, DriftPPM: 0, ChunkMS: 20},
		"loss":     {Seed: 6, DurationS: 5, LossBurst: 3, DriftPPM: 0, ChunkMS: 20},
	}
	for name, cfg := range classes {
		t.Run(name, func(t *testing.T) {
			rep, err := Run(cfg)
			if err != nil {
				t.Fatalf("%s: unexpected error %v", name, err)
			}
			if rep.AudioSamples <= 0 {
				t.Fatalf("%s: no audio samples", name)
			}
			if rep.EventSHA256 == "" {
				t.Fatalf("%s: empty event hash", name)
			}
			if rep.PipelineLatencyMSP95 > 50 {
				t.Fatalf("%s: latency p95 %.2f > 50ms", name, rep.PipelineLatencyMSP95)
			}
			if rep.SyncErrorMSP95 > 20 {
				t.Fatalf("%s: sync p95 %.2f > 20ms", name, rep.SyncErrorMSP95)
			}
			// determinism: the same class must hash identically on re-run
			again, _ := Run(cfg)
			if again.EventSHA256 != rep.EventSHA256 {
				t.Fatalf("%s: non-deterministic hash across runs", name)
			}
		})
	}
}

// TestRTPReorderWindowHonoursLimit ensures the adapter enforces the 512-packet
// reorder window ceiling as a hard, locatable rejection.
func TestRTPReorderWindowHonoursLimit(t *testing.T) {
	if _, err := adapter.NewRTPStream(1, 48000, 513); err == nil {
		t.Fatal("reorder window above 512 accepted")
	}
}

// TestVariableFrameRateScheduledByPTS verifies genuine VFR video (alternating
// 1/30 s and 1/15 s frame durations) is driven entirely by per-frame PTS and
// completes without timeline violations, while audio samples stay conserved.
func TestVariableFrameRateScheduledByPTS(t *testing.T) {
	cfg := Config{Seed: 7, DurationS: 4, LossBurst: 0, DriftPPM: 0, ChunkMS: 20}
	sender := NewSender(cfg)
	pkts := sender.GenerateVFR()
	senderRefs = sender.RefByExpandedSeq
	e := NewEngine(cfg)
	for _, rp := range pkts {
		e.ProcessPacket(rp)
	}
	e.Flush()
	rep := e.Finish()
	if rep.Status != statusOK {
		t.Fatalf("VFR run status=%s", rep.Status)
	}
	if rep.AudioSamples != int64(cfg.DurationS)*AudioClockRate {
		t.Fatalf("VFR audio sample conservation broken: %d", rep.AudioSamples)
	}
	// Alternating 3000/6000-tick frames average 22.5 fps, so 4 s holds about
	// 90 video frames, strictly fewer than the 120 a fixed 30 fps track would
	// produce; this proves scheduling follows per-frame PTS, not a cadence.
	videoFrames := int(e.VideoEventCount())
	if videoFrames >= int(cfg.DurationS)*30 {
		t.Fatalf("VFR frame count %d not below fixed-cadence %d",
			videoFrames, cfg.DurationS*30)
	}
	if videoFrames < int(cfg.DurationS)*15 {
		t.Fatalf("VFR frame count %d implausibly low", videoFrames)
	}
}

// TestUnifiedErrorChainRejectsBadTimeline confirms negative durations, PTS
// rollback and incomplete frames surface through the shared media error model
// rather than being silently committed.
func TestUnifiedErrorChainRejectsBadTimeline(t *testing.T) {
	s := pipeline.NewSession()
	base := pipeline.Event{
		TrackID: 3, Kind: pipeline.EventVideo, Seq: 0,
		PTSNanos: 100, DurationNanos: 10,
	}
	if err := s.Submit(base); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		ev   pipeline.Event
		code media.Code
	}{
		{"negative_duration", pipeline.Event{TrackID: 3, Seq: 1, PTSNanos: 200, DurationNanos: 0}, media.CodeNegativeDuration},
		{"pts_rollback", pipeline.Event{TrackID: 3, Seq: 2, PTSNanos: 50, DurationNanos: 10}, media.CodePTSRollback},
		{"incomplete_frame", pipeline.Event{TrackID: 3, Seq: 3, PTSNanos: 300, DurationNanos: 10, Incomplete: true}, media.CodeIncompleteFrame},
	}
	for _, c := range cases {
		err := s.Submit(c.ev)
		me, ok := media.AsError(err)
		if !ok || me.Code != c.code {
			t.Fatalf("%s: got %v want code %s", c.name, err, c.code)
		}
	}
	// rejected events must not have advanced the committed hash chain
	if s.Snapshot().EventCount != 1 {
		t.Fatal("rejected timeline events were committed")
	}
}
