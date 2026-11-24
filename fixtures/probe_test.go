package fixtures

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/mediapipe/media"
)

func gen(t *testing.T, spec Spec) string {
	t.Helper()
	res, err := Generate(spec, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return res.MainPath
}

func TestProbeHealthyContainer(t *testing.T) {
	path := gen(t, DefaultSpec("good.mp4"))
	c, err := media.Probe(path, media.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("tracks=%d", len(c.Tracks))
	}
	v := c.VideoTrack()
	if v == nil || v.SampleCount != 240 {
		t.Fatalf("video track: %+v", v)
	}
	if v.StartUS != 0 || v.EndUS != 8_000_000 {
		t.Fatalf("video span %d..%d", v.StartUS, v.EndUS)
	}
	if len(v.Keyframes) != 8 {
		t.Fatalf("keyframes=%d", len(v.Keyframes))
	}
	if v.FrameTicks != 1000 {
		t.Fatalf("frame ticks=%d", v.FrameTicks)
	}
	// Every absolute start is frameIndex/30 s in exact integer media time.
	for i, s := range v.Samples {
		want, err := media.FromTicks(int64(i)*1000, 30000)
		if err != nil {
			t.Fatal(err)
		}
		if s.StartUS != want.Micros() {
			t.Fatalf("sample %d start=%d want %d", i, s.StartUS, want.Micros())
		}
	}
	if len(c.Issues) != 0 {
		t.Fatalf("healthy input has issues: %+v", c.Issues)
	}
}

func TestProbeRejectsTruncatedContainer(t *testing.T) {
	path := gen(t, func() Spec { s := DefaultSpec("trunc.mp4"); s.Corrupt = "truncate_moov"; return s }())
	if _, err := media.Probe(path, media.DefaultLimits()); err == nil {
		t.Fatal("expected a classified error for truncated input")
	} else if media.Category(err) == "" {
		t.Fatalf("error is not classified: %v", err)
	}
}

func TestProbeRejectsCorruptBox(t *testing.T) {
	path := gen(t, func() Spec {
		s := DefaultSpec("corrupt.mp4")
		s.Corrupt = "corrupt_box"
		s.CorruptBox = "stco"
		return s
	}())
	_, err := media.Probe(path, media.DefaultLimits())
	if err == nil {
		t.Fatal("expected a classified error for corrupt box")
	}
}

func TestProbeFlagsZeroDuration(t *testing.T) {
	path := gen(t, func() Spec { s := DefaultSpec("zero.mp4"); s.ZeroDuration = true; return s }())
	c, err := media.Probe(path, media.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, is := range c.Issues {
		if is.Kind == media.KindZeroDuration {
			found = true
		}
	}
	if !found {
		t.Fatalf("zero-duration warning missing: %+v", c.Issues)
	}
	if c.MovieDurationUS != 0 {
		t.Fatalf("movie duration %d", c.MovieDurationUS)
	}
}

func TestProbeFlagsTimestampGap(t *testing.T) {
	path := gen(t, func() Spec { s := DefaultSpec("gap.mp4"); s.FrameDropEvery = 20; return s }())
	c, err := media.Probe(path, media.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, is := range c.Issues {
		if is.Kind == media.KindTimestampGap {
			found = true
		}
	}
	if !found {
		t.Fatalf("gap warning missing: %+v", c.Issues)
	}
}

func TestProbeFlagsWraparound(t *testing.T) {
	path := gen(t, func() Spec {
		s := DefaultSpec("wrap.mp4")
		s.Tracks = s.Tracks[:1]
		s.WrapAt = 20
		return s
	}())
	c, err := media.Probe(path, media.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, is := range c.Issues {
		if is.Kind == media.KindWraparound || is.Kind == media.KindNonMonotonic {
			found = true
		}
	}
	if !found {
		t.Fatalf("wraparound issue missing: %+v", c.Issues)
	}
	if v := c.VideoTrack(); v != nil {
		for i := 1; i < len(v.Samples); i++ {
			if v.Samples[i].StartUS > v.Samples[i-1].StartUS && v.Samples[i].StartUS == 0 {
				// no reordering happened
			}
		}
	}
	_ = os.ErrNotExist
	_ = filepath.Join
}
