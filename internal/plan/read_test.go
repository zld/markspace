package plan

import (
	"strings"
	"testing"
)

func validPlan() *Plan {
	return &Plan{Representations: []Representation{{
		ID: "r360", BandwidthBPS: 800000, Timescale: 300, StartTicks: 0,
		Segments: []Segment{
			{Number: 1, StartTicks: 0, DurationTicks: 3},
			{Number: 2, StartTicks: 3, DurationTicks: 3},
		},
	}}}
}

func TestParseOK(t *testing.T) {
	data := []byte(`{"representations":[{"id":"r","bandwidth_bps":1000,"timescale":300,"start_ticks":0,"segments":[{"number":1,"start_ticks":0,"duration_ticks":3}]}]}`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Representations[0].ID != "r" {
		t.Fatalf("unexpected id %q", p.Representations[0].ID)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	data := []byte(`{"representations":[],"bogus":1}`)
	if _, err := Parse(data); err == nil {
		t.Fatal("unknown JSON field must be rejected")
	}
}

func TestValidateFailures(t *testing.T) {
	cases := []struct {
		name string
		mut  func(p *Plan)
		want string
	}{
		{"empty", func(p *Plan) { p.Representations = nil }, "no representations"},
		{"empty id", func(p *Plan) { p.Representations[0].ID = "" }, "empty id"},
		{"dup id", func(p *Plan) {
			p.Representations = append(p.Representations, p.Representations[0])
		}, "duplicate representation id"},
		{"bad timescale", func(p *Plan) { p.Representations[0].Timescale = 0 }, "timescale"},
		{"bad bandwidth", func(p *Plan) { p.Representations[0].BandwidthBPS = -1 }, "bandwidth"},
		{"negative start", func(p *Plan) { p.Representations[0].StartTicks = -2 }, "start_ticks"},
		{"no segments", func(p *Plan) { p.Representations[0].Segments = nil }, "no segments"},
		{"dup number", func(p *Plan) {
			p.Representations[0].Segments[1].Number = 1
		}, "duplicate segment number"},
		{"out of order", func(p *Plan) {
			p.Representations[0].Segments[0].Number = 2
			p.Representations[0].Segments[1].Number = 1
		}, "out of order"},
		{"zero duration", func(p *Plan) {
			p.Representations[0].Segments[0].DurationTicks = 0
		}, "duration must be positive"},
		{"negative start ticks", func(p *Plan) {
			p.Representations[0].Segments[1].StartTicks = -1
		}, "negative start_ticks"},
		{"overflow end", func(p *Plan) {
			s := p.Representations[0].Segments
			s[0].StartTicks = 1<<63 - 1
			s[0].DurationTicks = 10
		}, "overflows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validPlan()
			tc.mut(p)
			err := p.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLimits(t *testing.T) {
	p := &Plan{}
	for i := 0; i < MaxRepresentations+1; i++ {
		p.Representations = append(p.Representations, Representation{
			ID: "r" + string(rune('a'+i)), BandwidthBPS: 1, Timescale: 1,
			Segments: []Segment{{Number: 1, StartTicks: 0, DurationTicks: 1}},
		})
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "exceed limit") {
		t.Fatalf("representation limit not enforced: %v", err)
	}

	rep := validPlan().Representations[0]
	rep.ID = "big"
	for i := 0; i < MaxSegmentsPerRep; i++ {
		rep.Segments = append(rep.Segments, Segment{Number: int64(i + 2), StartTicks: int64(i + 1), DurationTicks: 1})
	}
	p = &Plan{Representations: []Representation{rep}}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "segments exceed limit") {
		t.Fatalf("segment limit not enforced: %v", err)
	}
}
