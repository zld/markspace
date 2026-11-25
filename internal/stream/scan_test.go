package stream

import (
	"bytes"
	"testing"

	"replaycheck/internal/fault"
)

func buildFixture(t *testing.T, cfg Config) ([]byte, *Info) {
	t.Helper()
	img, err := Build(cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	info, err := ParseFile(bytes.NewReader(img), "fixture.mp4", int64(len(img)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return img, info
}

func TestParseSampleStructure(t *testing.T) {
	cfg := Config{
		Timescale: 90000, FPS: 10, Width: 64, Height: 48,
		SampleCount: 25, SampleSize: 80,
		Keyframes: []int{0, 10, 20},
	}
	img, info := buildFixture(t, cfg)
	if info.Timescale != 90000 {
		t.Fatalf("timescale = %d", info.Timescale)
	}
	tr := info.Track
	if len(tr.Samples) != 25 {
		t.Fatalf("samples = %d, want 25", len(tr.Samples))
	}
	if tr.Width != 64 || tr.Height != 48 {
		t.Fatalf("dimensions = %dx%d", tr.Width, tr.Height)
	}
	if got, want := len(info.KeyframeTicks), 3; got != want {
		t.Fatalf("keyframes = %d, want %d", got, want)
	}
	for i, want := range []int64{0, 90000, 180000} {
		if info.KeyframeTicks[i] != want {
			t.Fatalf("keyframe %d = %d, want %d", i, info.KeyframeTicks[i], want)
		}
	}
	for i, want := range []int64{90000, 90000} {
		if info.KeyframeIntervals[i] != want {
			t.Fatalf("interval %d = %d, want %d", i, info.KeyframeIntervals[i], want)
		}
	}
	// Every sample's declared range must fall inside the file and carry the
	// payload the builder wrote.
	for _, s := range tr.Samples {
		if s.Offset < 0 || s.Offset+s.Size > int64(len(img)) {
			t.Fatalf("sample %d range [%d,%d) outside %d", s.Index, s.Offset, s.Offset+s.Size, len(img))
		}
		pay := img[s.Offset : s.Offset+s.Size]
		if int(pay[0])<<8|int(pay[1]) != s.Index {
			t.Fatalf("sample %d payload carries index %d", s.Index, int(pay[0])<<8|int(pay[1]))
		}
	}
	if info.FirstBadOffset != -1 {
		t.Fatalf("clean file reported bad offset %d", info.FirstBadOffset)
	}
}

func TestTruncatedFileReportsFirstUnreadable(t *testing.T) {
	cfg := Config{
		Timescale: 90000, FPS: 10, Width: 64, Height: 48,
		SampleCount: 20, SampleSize: 100, KeyEvery: 5, TruncateBytes: 450,
	}
	img, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	info, err := ParseFile(bytes.NewReader(img), "cut.mp4", int64(len(img)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.FirstBadOffset < 0 {
		t.Fatal("expected an unreadable sample offset")
	}
	iss, ok := info.Issues.First(fault.KindUnreadable)
	if !ok {
		t.Fatalf("missing unreadable issue: %v", &info.Issues)
	}
	if iss.Where.ByteOffset != info.FirstBadOffset {
		t.Fatalf("issue offset %d != first bad %d", iss.Where.ByteOffset, info.FirstBadOffset)
	}
	// Every sample before the first unreadable one must be fully present.
	for _, s := range info.Track.Samples {
		if s.Offset+s.Size > int64(len(img)) {
			if s.Offset < info.FirstBadOffset {
				t.Fatalf("sample %d at %d is bad but precedes reported offset %d", s.Index, s.Offset, info.FirstBadOffset)
			}
		}
	}
}

func TestKeyframeIntervalUniform(t *testing.T) {
	_, info := buildFixture(t, Config{
		Timescale: 90000, FPS: 30, SampleCount: 60, KeyEvery: 15,
	})
	if len(info.KeyframeIntervals) != 3 {
		t.Fatalf("intervals = %d, want 3", len(info.KeyframeIntervals))
	}
	for _, d := range info.KeyframeIntervals {
		if d != 45000 {
			t.Fatalf("interval = %d, want 45000", d)
		}
	}
}

func TestNoKeyframesFlagged(t *testing.T) {
	img, err := Build(Config{Timescale: 90000, FPS: 10, SampleCount: 5, Keyframes: []int{-1}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := ParseFile(bytes.NewReader(img), "nokey.mp4", int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Issues.HasKind(fault.KindMissingKeyframe) {
		t.Fatal("expected missing-keyframe issue")
	}
}

func TestGarbageRejected(t *testing.T) {
	junk := bytes.Repeat([]byte{0xFF}, 512)
	if _, err := ParseFile(bytes.NewReader(junk), "junk.mp4", 512); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestTimelineMatchesTrack(t *testing.T) {
	_, info := buildFixture(t, Config{Timescale: 90000, FPS: 25, SampleCount: 50})
	tl, err := info.Timeline(48000)
	if err != nil {
		t.Fatal(err)
	}
	if tl.Rate().Num != 90000 {
		t.Fatalf("tick rate = %v", tl.Rate())
	}
	if got := tl.TicksToMillis(90000); got != 1000 {
		t.Fatalf("90000 ticks = %dms", got)
	}
	if got := tl.TicksToSamples(90000); got != 48000 {
		t.Fatalf("90000 ticks = %d samples", got)
	}
}
