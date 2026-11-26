package timeline

import (
	"errors"
	"testing"

	"assetpack/internal/clock"
)

func validSequence() Sequence {
	return Sequence{
		ID: 1, Duration: 180000,
		Clips: []Clip{
			{ID: "v1", Track: "video", Start: 0, SourceStart: 0, SourceEnd: 90000, SourceDuration: 180000, Grid: 3750},
			{ID: "v2", Track: "video", Start: 90000, SourceStart: 90000, SourceEnd: 180000, SourceDuration: 180000, Grid: 3750},
			{ID: "a1", Track: "audio", Start: 0, SourceStart: 0, SourceEnd: 180000, SourceDuration: 180000, Grid: 15},
		},
		Transitions: []Transition{
			{ID: "xfade", Track: "video", Kind: "crossfade", FromClip: "v1", ToClip: "v2", EditPoint: 90000, Duration: 7500, Grid: 3750},
		},
	}
}

func TestCompileCanonicalTimeline(t *testing.T) {
	tl, err := Compile(validSequence())
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Tracks) != 2 || len(tl.Transitions) != 1 {
		t.Fatalf("tracks=%d transitions=%d", len(tl.Tracks), len(tl.Transitions))
	}
	tr := tl.Transitions[0]
	if tr.Span.From != 86250 || tr.Span.To != 93750 {
		t.Fatalf("bad transition span %+v", tr.Span)
	}
}

func TestCompileRejectsOutOfBoundsBackwardsAndMisalignment(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Sequence)
	}{
		{"out of bounds", func(s *Sequence) { s.Clips[1].SourceEnd = 180001 }},
		{"backwards", func(s *Sequence) { s.Clips[1].Start = 1 }},
		{"misaligned", func(s *Sequence) { s.Clips[1].Start = 90001 }},
		{"bad transition", func(s *Sequence) { s.Transitions[0].EditPoint = 90001 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSequence()
			tc.mut(&s)
			_, err := Compile(s)
			if err == nil {
				t.Fatal("expected compile error")
			}
			var le *clock.Error
			if !errors.As(err, &le) || le.Stage == "" || le.Track == "" {
				t.Fatalf("error must carry track/stage/offset: %v", err)
			}
		})
	}
}
