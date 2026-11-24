package recovery

import (
	"testing"

	"medcheck/internal/clock"
	"medcheck/internal/jitter"
)

func TestRecovererFillsSmallAudioGap(t *testing.T) {
	r := New(Config{})
	ev := jitter.Event{Kind: jitter.EventGap, Stream: clock.StreamAudio, SSRC: 1, Seqs: []uint64{8}, PTSNS: 160_000_000}
	out := r.Handle(ev)
	if len(out) != 1 || !out[0].Recovered || out[0].Strategy != StrategyAudioSilencePLC {
		t.Fatalf("out=%+v", out)
	}
	if r.Stats.Recovered != 1 || len(r.Errors) != 1 || r.Errors[0].Code != clock.CodeAudioRecovered {
		t.Fatalf("stats=%+v errors=%v", r.Stats, r.Errors)
	}
}

func TestRecovererRejectsLargeAudioGap(t *testing.T) {
	r := New(Config{MaxAudioGapPackets: 1})
	ev := jitter.Event{Kind: jitter.EventGap, Stream: clock.StreamAudio, Seqs: []uint64{8, 9}}
	if out := r.Handle(ev); out != nil {
		t.Fatalf("expected no synthetic audio for over-policy gap: %+v", out)
	}
	if r.Stats.AudioUnrecoverable != 1 {
		t.Fatalf("stats=%+v", r.Stats)
	}
}

func TestVideoGapWaitsForRealKeyframeOnly(t *testing.T) {
	r := New(Config{})
	gap := jitter.Event{Kind: jitter.EventGap, Stream: clock.StreamVideo, SSRC: 7, Seqs: []uint64{20}}
	if out := r.Handle(gap); len(out) != 1 || !out[0].AwaitKeyframe || out[0].Keyframe {
		t.Fatalf("gap marker must not be a fake keyframe: %+v", out)
	}
	if !r.AwaitingKeyframe(7) {
		t.Fatal("expected keyframe wait")
	}

	pFrame := jitter.Event{Kind: jitter.EventData, Packet: &jitter.TimedPacket{
		Stream: clock.StreamVideo, SSRC: 7, ExtSeq: 21,
	}}
	if out := r.Handle(pFrame); out != nil {
		t.Fatalf("P-frame after gap must be dropped: %+v", out)
	}
	key := jitter.Event{Kind: jitter.EventData, Packet: &jitter.TimedPacket{
		Stream: clock.StreamVideo, SSRC: 7, ExtSeq: 22, Keyframe: true,
	}}
	out := r.Handle(key)
	if len(out) != 1 || !out[0].Keyframe {
		t.Fatalf("real keyframe should resume output: %+v", out)
	}
	if r.Stats.DroppedFrames != 1 {
		t.Fatalf("stats=%+v", r.Stats)
	}
	if finish := r.Finish(); len(finish) != 0 {
		t.Fatalf("resolved wait should not be unrecoverable: %v", finish)
	}
}

func TestUnresolvedVideoGapIsUnrecoverable(t *testing.T) {
	r := New(Config{})
	r.Handle(jitter.Event{Kind: jitter.EventGap, Stream: clock.StreamVideo, SSRC: 9, Seqs: []uint64{3}})
	errs := r.Finish()
	if len(errs) != 1 || errs[0].Code != clock.CodeVideoUnrecoverable {
		t.Fatalf("errs=%v", errs)
	}
}
