package recovery

import (
	"errors"
	"testing"

	"mediapipeline/internal/clock"
	mpierr "mediapipeline/internal/errors"
	"mediapipeline/internal/jitter"
	"mediapipeline/internal/rtp"
)

func newRec(t *testing.T, policy Policy) (*Recoverer, *clock.Timeline) {
	t.Helper()
	tl := clock.NewTimeline(clock.DefaultTickRateHz)
	r, err := New(Config{Codec: CodecOpus, Policy: policy, Node: "recover", SampleRate: 48000}, tl)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return r, tl
}

func frameEvent(seq uint64, samples uint16) jitter.Event {
	return jitter.Event{Frame: &rtp.Frame{ExtSeq: seq, Samples: samples, StreamOffset: 0}}
}

func gapEvent(seq uint64) jitter.Event {
	return jitter.Event{Gap: &jitter.Gap{Seq: seq, Node: "jitter"}}
}

func TestFrameCommitsTimeline(t *testing.T) {
	r, tl := newRec(t, PolicySkipGaps)
	out, err := r.Handle(frameEvent(0, 960))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if out.Interval.Start != 0 || out.Interval.End != 960 || tl.Cursor(clock.KindAudio) != 960 {
		t.Fatalf("interval = %+v cursor = %d", out.Interval, tl.Cursor(clock.KindAudio))
	}
}

func TestVariableLengthFrames(t *testing.T) {
	r, _ := newRec(t, PolicySkipGaps)
	o0, _ := r.Handle(frameEvent(0, 480))
	o1, _ := r.Handle(frameEvent(1, 1920))
	if o0.Interval.Len() != 480 || o1.Interval.Len() != 1920 || o1.Interval.Start != 480 {
		t.Fatalf("variable length intervals wrong: %+v %+v", o0.Interval, o1.Interval)
	}
}

func TestGapStrictFails(t *testing.T) {
	r, _ := newRec(t, PolicyStrict)
	_, err := r.Handle(gapEvent(3))
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatGap || me.FirstSeq != 3 {
		t.Fatalf("err = %v, want CatGap at seq 3", err)
	}
}

func TestGapSkipReservesButInventsNothing(t *testing.T) {
	r, tl := newRec(t, PolicySkipGaps)
	out, err := r.Handle(gapEvent(7))
	if err != nil {
		t.Fatalf("recoverable gap: %v", err)
	}
	if out.Frame != nil || out.Gap == nil || out.Gap.Seq != 7 {
		t.Fatalf("gap outcome must carry no frame: %+v", out)
	}
	// 20 ms Opus estimate at 48 kHz = 960 ticks reserved.
	if out.Interval.Len() != 960 || tl.Cursor(clock.KindAudio) != 960 {
		t.Fatalf("gap reservation = %+v", out.Interval)
	}
	gaps := r.Gaps()
	if len(gaps) != 1 || gaps[0].EstimatedSamples != 960 {
		t.Fatalf("gap log = %+v", gaps)
	}
	// Timeline continues seamlessly after the gap.
	next, _ := r.Handle(frameEvent(8, 960))
	if next.Interval.Start != 960 {
		t.Fatalf("post-gap start = %d, want 960", next.Interval.Start)
	}
}

func TestAACEstimate(t *testing.T) {
	tl := clock.NewTimeline(clock.DefaultTickRateHz)
	r, _ := New(Config{Codec: CodecAAC, Policy: PolicySkipGaps, SampleRate: 44100}, tl)
	out, err := r.Handle(gapEvent(1))
	if err != nil {
		t.Fatalf("aac gap: %v", err)
	}
	// 1024 samples at 44.1 kHz mapped to 48 kHz ticks.
	want := tl.Convert(1024, 44100)
	if out.Interval.Len() != want {
		t.Fatalf("aac gap ticks = %d, want %d", out.Interval.Len(), want)
	}
}

func TestCorruptAlwaysFatal(t *testing.T) {
	r, _ := newRec(t, PolicySkipGaps)
	in := mpierr.New(mpierr.CatCorrupt, 9, "", "bad crc").AtOffset(55)
	out := r.RejectCorrupt(in)
	if out.Category != mpierr.CatCorrupt || out.Node != "recover" || out.ByteOffset != 55 {
		t.Fatalf("corrupt error altered: %+v", out)
	}
}

func TestZeroSampleRateRejected(t *testing.T) {
	if _, err := New(Config{SampleRate: 0}, clock.NewTimeline(48000)); err == nil {
		t.Fatal("zero sample rate must be rejected")
	}
}
