package container

import (
	"bytes"
	"strings"
	"testing"

	"packcheck/internal/model"
)

func TestBuildParseRoundTrip(t *testing.T) {
	raw, err := Build(BuildOptions{
		Timescale: 25, Samples: 100, SegmentFrames: 25, StartTick: 10,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	c, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.MovieTimescale != 25 {
		t.Errorf("movie timescale=%d", c.MovieTimescale)
	}
	if len(c.Tracks) != 1 || c.Tracks[0].Timescale != 25 || c.Tracks[0].Samples != 100 {
		t.Errorf("track view wrong: %+v", c.Tracks)
	}
	if c.Tracks[0].StartTick != 10 {
		t.Errorf("edit-list start=%d want 10", c.Tracks[0].StartTick)
	}
	if len(c.Boundaries) != 4 || c.Boundaries[3].EndTick != 100 {
		t.Errorf("boundaries wrong: %+v", c.Boundaries)
	}
	res := Validate(c, 1)
	if res.Issues.HasError() {
		t.Errorf("healthy container produced issues: %v", res.Issues.All())
	}
}

func TestMissingTrackCounted(t *testing.T) {
	raw, _ := Build(BuildOptions{Timescale: 25, Samples: 50, SegmentFrames: 25})
	c, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	res := Validate(c, 2)
	counts := res.Issues.CountByCode()
	if counts[model.CodeMissingTrack] != 1 {
		t.Errorf("missing-track count=%d", counts[model.CodeMissingTrack])
	}
}

func TestTimebaseMismatchCounted(t *testing.T) {
	raw, err := Build(BuildOptions{
		Timescale: 25, Samples: 50, SegmentFrames: 25, BadTimebase: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	res := Validate(c, 1)
	counts := res.Issues.CountByCode()
	if counts[model.CodeTimebaseMismatch] != 1 {
		t.Errorf("timebase mismatch count=%d issues=%v", counts[model.CodeTimebaseMismatch], res.Issues.All())
	}
}

func TestBoundaryDriftAndTruncationCounted(t *testing.T) {
	for _, opt := range []BuildOptions{
		{Timescale: 25, Samples: 100, SegmentFrames: 25, Drift: 3},
		{Timescale: 25, Samples: 100, SegmentFrames: 25, Truncate: true},
	} {
		raw, err := Build(opt)
		if err != nil {
			t.Fatal(err)
		}
		c, err := Parse(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		res := Validate(c, 1)
		if res.Issues.CountByCode()[model.CodeBoundaryDrift] != 1 {
			t.Errorf("opt=%+v expected exactly one boundary drift, got %v",
				opt, res.Issues.All())
		}
	}
}

func TestGarbageContainerFailsHard(t *testing.T) {
	if _, err := Parse(bytes.NewReader([]byte("not an mp4 at all"))); err == nil {
		t.Error("garbage input must return a parse error")
	}
	if _, err := Parse(strings.NewReader("")); err == nil {
		t.Error("empty input must return a parse error")
	}
}

func TestDefectClassesAreSeparatelyCounted(t *testing.T) {
	raw, _ := Build(BuildOptions{
		Timescale: 25, Samples: 100, SegmentFrames: 25,
		BadTimebase: 30, Drift: 2,
	})
	c, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	res := Validate(c, 1)
	counts := res.Issues.CountByCode()
	if counts[model.CodeTimebaseMismatch] != 1 || counts[model.CodeBoundaryDrift] != 1 {
		t.Errorf("defect classes merged: %v", counts)
	}
}
