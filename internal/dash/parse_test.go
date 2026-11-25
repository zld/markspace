package dash

import (
	"testing"

	"mediasync/internal/model"
)

// validFixture is generated in-test (no external files): two periods, each
// with a video and an audio representation on different timescales.
func validFixture() []byte {
	return []byte(`# generated DASH fixture
PERIOD 0
REP video-hi video 1 90000
SEG 0 0 90000 keyframe
SEG 1 90000 90000
SEG 2 180000 45000 keyframe
REP dialogue-core dialogue 0 48000
SEG 0 0 48000
SEG 1 48000 48000
PERIOD 1
REP video-hi video 1 90000
SEG 0 0 90000 keyframe
REP dialogue-core dialogue 0 48000
SEG 0 0 24000
`)
}

func TestParseHappyPath(t *testing.T) {
	m := Parse(validFixture())
	if len(m.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", m.Errors)
	}
	if len(m.Periods) != 2 {
		t.Fatalf("periods = %d, want 2", len(m.Periods))
	}
	p0 := m.Periods[0]
	if len(p0.Representations) != 2 {
		t.Fatalf("reps = %d, want 2", len(p0.Representations))
	}
	video := p0.Representations[0]
	if video.ID != "video-hi" || video.Timescale != 90000 || len(video.Segments) != 3 {
		t.Fatalf("video rep mismatch: %+v", video)
	}
	// 90000 units @90kHz == 1 second on the shared base.
	if video.Segments[0].StartNS != 0 || video.Segments[0].DurationNS != 1_000_000_000 {
		t.Fatalf("timescale mapping wrong: %+v", video.Segments[0])
	}
	if video.Segments[2].StartNS != 2_000_000_000 || !video.Segments[2].Keyframe {
		t.Fatalf("third segment timing/keyframe wrong: %+v", video.Segments[2])
	}
	audio := p0.Representations[1]
	if audio.Priority != model.PriorityCore || audio.Segments[1].StartNS != 1_000_000_000 {
		t.Fatalf("core audio mapping wrong: %+v", audio)
	}
	// Period 1 restarts its local timeline at zero; PeriodID must be carried.
	if m.Periods[1].Representations[0].Segments[0].PeriodID != 1 {
		t.Fatal("period id not propagated to descriptor")
	}
}

func TestParseRejectsIllegalTiming(t *testing.T) {
	fixture := []byte(`PERIOD 0
REP bad-ts video 1 0
SEG 0 0 1000
REP neg dialogue 0 48000
SEG 0 -5 48000
REP zerodur subtitle 0 1000
SEG 0 0 0
REP overflow sfx 2 1
SEG 0 9223372036854775807 1
REP good video 1 1000
SEG 0 0 1000 keyframe
`)
	m := Parse(fixture)
	// The zero-timescale REP is rejected wholesale; the remaining four register.
	if len(m.Periods[0].Representations) != 4 {
		t.Fatalf("valid reps should still be registered, got %d", len(m.Periods[0].Representations))
	}
	kinds := map[model.ErrorKind]bool{}
	for _, e := range m.Errors {
		kinds[e.Kind] = true
		if e.Offset <= 0 {
			t.Fatalf("error must carry 1-based line location: %v", e)
		}
	}
	for _, want := range []model.ErrorKind{
		model.KindInvalidTimescale, model.KindNegativeDuration,
		model.KindNegativeDuration, model.KindOverflow} {
		if !kinds[want] {
			t.Fatalf("missing expected error kind %s, got %v", want, m.Errors)
		}
	}
	// The healthy representation still parses end to end.
	good := m.Periods[0].Representations[3]
	if len(good.Segments) != 1 || good.Segments[0].DurationNS != 1_000_000_000 {
		t.Fatalf("healthy rep must survive neighbors' failures: %+v", good)
	}
}

func TestParseStructuralErrorsAndDeterminism(t *testing.T) {
	fixture := []byte(`SEG 0 0 1000
PERIOD 0
REP only video 1
SEG 0 0 1000
WAT 1 2
PERIOD 1
REP v video 1 1000
SEG 0 0 500
`)
	first := Parse(fixture)
	second := Parse(fixture)
	if len(first.Errors) != len(second.Errors) {
		t.Fatal("error count must be stable across runs")
	}
	for i := range first.Errors {
		if first.Errors[i].Error() != second.Errors[i].Error() {
			t.Fatalf("error %d differs across runs", i)
		}
	}
	if len(first.Errors) != 4 {
		t.Fatalf("want 4 structural errors, got %d: %v", len(first.Errors), first.Errors)
	}
	if first.Periods[1].Representations[0].Segments[0].DurationNS != 500_000_000 {
		t.Fatal("valid tail must parse despite earlier errors")
	}
}
