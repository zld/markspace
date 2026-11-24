package probe

import (
	"testing"

	"archive/internal/clock"
	"archive/internal/fixture"
)

func mxfWithDeclarations() fixture.MXFSpec {
	s := fixture.DefaultMXFSpec()
	s.Tracks[0].Rotation = 90
	s.Tracks[0].Crop = &fixture.MXFCrop{Left: 0, Right: 1440, Top: 0, Bottom: 1080}
	s.Tracks[0].Color = &fixture.MXFColor{Primaries: 1, Transfer: 1, Matrix: 1}
	s.Tracks[0].Clips = []fixture.MXFClip{
		{StartFrame: 0, FrameCount: 20},
		{StartFrame: 20, FrameCount: 30},
	}
	return s
}

func TestProbeMXFDeclarations(t *testing.T) {
	c, err := ProbeMXF(fixture.BuildMXF(mxfWithDeclarations()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Format != "mxf" {
		t.Fatalf("format = %q", c.Format)
	}
	tr := c.Tracks[0]
	if tr.StoredWidth != 1920 || tr.StoredHeight != 1080 {
		t.Fatalf("stored = %dx%d", tr.StoredWidth, tr.StoredHeight)
	}
	if tr.SampleIntervalNS != 40000000 || tr.DurationNS != 2000000000 {
		t.Fatalf("interval=%d duration=%d", tr.SampleIntervalNS, tr.DurationNS)
	}
	if len(tr.Rotations) != 1 || tr.Rotations[0].Degrees != 90 || tr.Rotations[0].Source != SourceMXFRot {
		t.Fatalf("rotations = %+v", tr.Rotations)
	}
	if len(tr.Crops) != 1 || tr.Crops[0].Width() != 1440 {
		t.Fatalf("crops = %+v", tr.Crops)
	}
	if len(tr.Colors) != 1 || tr.Colors[0].Primaries != 1 {
		t.Fatalf("colors = %+v", tr.Colors)
	}
	if len(tr.Clips) != 2 || tr.Clips[1].StartNS != 800000000 || tr.Clips[1].DurationNS != 1200000000 {
		t.Fatalf("clips = %+v", tr.Clips)
	}
}

func TestProbeMXFTruncated(t *testing.T) {
	s := fixture.DefaultMXFSpec()
	s.TruncateBytes = 40
	_, err := ProbeMXF(fixture.BuildMXF(s))
	if got := clock.AsKind(err); got != clock.KindTruncated {
		t.Fatalf("kind = %q err = %v", got, err)
	}
}

func TestProbeMXFCorruptIndex(t *testing.T) {
	s := fixture.DefaultMXFSpec()
	s.CorruptIndex = true
	_, err := ProbeMXF(fixture.BuildMXF(s))
	if got := clock.AsKind(err); got != clock.KindOutOfRange {
		t.Fatalf("kind = %q err = %v", got, err)
	}
}

func TestSniffDispatches(t *testing.T) {
	mp4c, err := Sniff(fixture.BuildMP4(baseSpec()))
	if err != nil || mp4c.Format != "mp4" {
		t.Fatalf("mp4 sniff: %v %+v", err, mp4c)
	}
	mxfc, err := Sniff(fixture.BuildMXF(fixture.DefaultMXFSpec()))
	if err != nil || mxfc.Format != "mxf" {
		t.Fatalf("mxf sniff: %v %+v", err, mxfc)
	}
	if _, err := Sniff([]byte("not a container")); clock.AsKind(err) != clock.KindInvalidMetadata {
		t.Fatalf("junk kind = %q", clock.AsKind(err))
	}
}
