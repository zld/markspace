package ingest

import (
	"strings"
	"testing"

	"mediad/internal/mederr"
)

func validTestEDL() *EDL {
	return &EDL{Segments: []Segment{
		{Track: "a", StartNanos: 0, DurationNanos: 40},
		{Track: "b", StartNanos: 35, DurationNanos: 40, TransitionInNanos: 5},
		{Track: "a", StartNanos: 70, DurationNanos: 40, TransitionInNanos: 5},
	}}
}

func TestEDLValidateAcceptsDeclaredTransitions(t *testing.T) {
	if err := validTestEDL().Validate([]string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
}

func TestEDLValidateLocatesStrictFailures(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(e *EDL)
		track  string
	}{
		{"missing track", func(e *EDL) { e.Segments[1].Track = "ghost" }, "ghost"},
		{"negative duration", func(e *EDL) { e.Segments[1].DurationNanos = -1 }, "b"},
		{"contradictory boundary", func(e *EDL) { e.Segments[1].StartNanos = 30 }, "b"},
		{"first transition", func(e *EDL) { e.Segments[0].TransitionInNanos = 2 }, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := validTestEDL()
			tc.mutate(e)
			err := e.Validate([]string{"a", "b"})
			if err == nil {
				t.Fatal("expected validation failure")
			}
			me, ok := err.(*mederr.Error)
			if !ok || me.Track != tc.track || me.Recoverable {
				t.Fatalf("error not strictly located/fatal: %+v", err)
			}
		})
	}
}

func TestParseEDLRejectsUnknownFields(t *testing.T) {
	_, err := ParseEDL(strings.NewReader(`{"segments":[],"typo":1}`))
	if err == nil {
		t.Fatal("expected unknown-field parse error")
	}
}
