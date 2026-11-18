package input

import (
	"strings"
	"testing"

	"dashforge/internal/model"
)

func validMeta() *model.Metadata {
	return &model.Metadata{
		Title: "t",
		Renditions: []model.Rendition{{
			ID: "r1", Width: 640, Height: 360, Bandwidth: 800_000,
			Codec: "avc1.64001e", FrameRate: "30/1", StartOffsetMs: 0,
			SegmentDurationMs: 4000, Media: "v/$RepresentationID$/$Number$.m4s",
			SegmentRoot: "out",
			Segments: []model.Segment{
				{Number: 1, StartMs: 0, DurationMs: 4000},
				{Number: 2, StartMs: 4000, DurationMs: 3999},
			},
		}},
	}
}

func TestValidateOK(t *testing.T) {
	if err := Validate(validMeta(), model.DefaultMaxRepresentations, model.DefaultMaxSegments); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeRejectsBrokenJSON(t *testing.T) {
	cases := map[string]string{
		"truncated":     `{"title":"x","renditions":[`,
		"trailing":      `{"renditions":[]} {}`,
		"unknown field": `{"bogus":1}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(doc), 32, 10000); err == nil {
				t.Fatal("expected error for malformed document")
			}
		})
	}
}

func TestValidateReportsRenditionAndField(t *testing.T) {
	m := validMeta()
	rd := &m.Renditions[0]
	rd.ID = ""
	rd.Width = 0
	rd.Bandwidth = -5
	rd.Codec = "wmv3"
	rd.Segments[1].StartMs = -1

	err := Validate(m, 32, 10000)
	var errs Errors
	if !asErrors(err, &errs) {
		t.Fatalf("expected input.Errors, got %T", err)
	}
	joined := err.Error()
	for _, want := range []string{"<rendition[0]>", "width", "bandwidth_bps", "codec", "start_ms"} {
		if !strings.Contains(joined, want) {
			t.Errorf("error %q missing %q", joined, want)
		}
	}
}

func TestValidateDuplicatesAndLimits(t *testing.T) {
	m := validMeta()
	dup := m.Renditions[0]
	dup.ID = "r1"
	m.Renditions = append(m.Renditions, dup)
	err := Validate(m, 32, 10000)
	if err == nil || !strings.Contains(err.Error(), "duplicate rendition id") {
		t.Fatalf("expected duplicate id error, got %v", err)
	}

	lim := Validate(validMeta(), 0, 10000)
	if lim == nil || !strings.Contains(lim.Error(), "exceeds limit") {
		t.Fatalf("expected representation limit error, got %v", lim)
	}

	m2 := validMeta()
	m2.Renditions[0].Segments = []model.Segment{{Number: 1, StartMs: 0, DurationMs: 1}}
	if err := Validate(m2, 32, 0); err == nil {
		t.Fatal("expected segment limit error")
	}
}

func TestValidateSegmentRules(t *testing.T) {
	m := validMeta()
	m.Renditions[0].Segments[1].Number = 1 // duplicate number
	m.Renditions[0].Segments[1].DurationMs = 0
	err := Validate(m, 32, 10000)
	got := err.Error()
	if !strings.Contains(got, "duplicate segment number") || !strings.Contains(got, "duration") {
		t.Fatalf("expected segment errors, got %v", err)
	}
}

func TestCodecWhitelist(t *testing.T) {
	for _, c := range []string{"avc1", "avc1.640028", "hvc1.1.6.L93", "av01.0.04M.08", "mp4a.40.2"} {
		if !codecSupported(c) {
			t.Errorf("codec %q should be supported", c)
		}
	}
	for _, c := range []string{"", "wmv3", "mp3"} {
		if codecSupported(c) {
			t.Errorf("codec %q should be rejected", c)
		}
	}
}

func asErrors(err error, target *Errors) bool {
	es, ok := err.(Errors)
	if !ok {
		return false
	}
	*target = es
	return true
}
