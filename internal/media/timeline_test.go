package media

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildTimeline(t *testing.T) *Timeline {
	t.Helper()
	tb, _ := NewTimeBase(1000) // 1 tick = 1 ms = 1_000_000 ns
	video := &Track{Key: "video", TimeBase: tb}
	audio := &Track{Key: "audio", TimeBase: tb}
	for i := 0; i < 4; i++ {
		start := int64(i) * NanoPerSecond
		if err := video.Add(start, NanoPerSecond, 0, 8, int64(i), nil); err != nil {
			t.Fatal(err)
		}
		if err := audio.Add(start, NanoPerSecond, 0, 4, int64(i), nil); err != nil {
			t.Fatal(err)
		}
	}
	return NewTimeline([]*Track{audio, video}) // intentionally unordered
}

func TestNewTimelineSortsTracks(t *testing.T) {
	tl := buildTimeline(t)
	if tl.Tracks[0].Key != "audio" || tl.Tracks[1].Key != "video" {
		t.Fatalf("tracks not key-sorted: %s, %s", tl.Tracks[0].Key, tl.Tracks[1].Key)
	}
	if tl.SampleCount() != 8 {
		t.Fatalf("samples = %d, want 8", tl.SampleCount())
	}
	if d, _ := tl.DurationNs(); d != 4*NanoPerSecond {
		t.Fatalf("duration = %d", d)
	}
}

func TestTrackRejectsOverlapAndZeroDuration(t *testing.T) {
	tr := &Track{Key: "x"}
	if err := tr.Add(0, 10, 0, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := tr.Add(5, 10, 0, 1, 1, nil); err == nil {
		t.Fatal("overlapping sample must be rejected")
	}
	if err := tr.Add(10, 0, 0, 1, 1, nil); err == nil {
		t.Fatal("zero-duration sample must be rejected")
	}
}

func TestClipOutOfBoundsRejected(t *testing.T) {
	tl := buildTimeline(t)
	tl.Clips = []Clip{{TrackKey: "video", Interval: Interval{0, 5 * NanoPerSecond}}}
	err := tl.ValidateClips()
	var me *Error
	if !errors.As(err, &me) {
		t.Fatalf("want *Error, got %v", err)
	}
	if me.Stage != StageScan || me.Track != "video" || me.InputOffset != 5*NanoPerSecond {
		t.Fatalf("error not locatable: %+v", me)
	}
}

func TestTransitionBoundaryRejected(t *testing.T) {
	tl := buildTimeline(t)
	// Self-transition.
	tl.Transitions = []Transition{{FromTrack: "video", ToTrack: "video",
		Interval: Interval{0, NanoPerSecond}}}
	if err := tl.ValidateTransitions(); err == nil {
		t.Fatal("self-transition must be rejected")
	}
	// Out-of-bounds end on the destination track.
	tl.Transitions = []Transition{{FromTrack: "video", ToTrack: "audio",
		Interval: Interval{0, 9 * NanoPerSecond}}}
	err := tl.ValidateTransitions()
	var me *Error
	if !errors.As(err, &me) || me.InputOffset != 9*NanoPerSecond {
		t.Fatalf("want offset on structured error, got %v", err)
	}
}

func TestEDLRoundTripAndOffsets(t *testing.T) {
	tl := buildTimeline(t)
	tl.Clips = []Clip{{TrackKey: "video", Interval: Interval{0, NanoPerSecond}}}
	tl.Transitions = []Transition{{FromTrack: "video", ToTrack: "audio",
		Interval: Interval{NanoPerSecond, 2 * NanoPerSecond}}}
	text := EDLText(tl)

	parsed := NewTimeline([]*Track{tl.Tracks[0], tl.Tracks[1]})
	if err := ParseEDL(strings.NewReader(string(text)), parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Clips) != 1 || len(parsed.Transitions) != 1 {
		t.Fatal("EDL round trip lost directives")
	}

	// A bad second line must report its absolute byte offset.
	bad := string(text) + "clip video 100 50\n"
	err := ParseEDL(strings.NewReader(bad), NewTimeline(nil))
	var me *Error
	if !errors.As(err, &me) || me.InputOffset != int64(len(text)) {
		t.Fatalf("offset = %d, want %d (err=%v)", me.InputOffset, len(text), err)
	}
}

func TestSafeOpenRejectsSymlinkAndTraversal(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.bin")
	if err := os.WriteFile(real, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.bin")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeOpen(dir, "link.bin"); err == nil {
		t.Fatal("symlink open must be rejected")
	}
	if _, err := SafeOpen(dir, "../escape.bin"); err == nil {
		t.Fatal("traversal path must be rejected")
	}
	f, err := SafeOpen(dir, "real.bin")
	if err != nil {
		t.Fatalf("legitimate open failed: %v", err)
	}
	_ = f.Close()
}

func TestOrderedSamplesStable(t *testing.T) {
	tl := buildTimeline(t)
	got := tl.OrderedSamples()
	if len(got) != 8 {
		t.Fatalf("ordered samples = %d", len(got))
	}
	if got[0].Track != "audio" || got[4].Track != "video" {
		t.Fatal("canonical order must follow sorted track keys")
	}
}

func TestErrorRendering(t *testing.T) {
	e := NewError(StageScan, "op", "boom").WithTrack("v").WithOffset(42)
	s := e.Error()
	if !strings.Contains(s, "scan") || !strings.Contains(s, "track=v") ||
		!strings.Contains(s, "offset=42") {
		t.Fatalf("error line missing fields: %s", s)
	}
	if ClassifyExit(e) != ExitInvalidInput {
		t.Fatal("scan error must map to exit 2")
	}
	r := NewError(StageRecover, "verify", "hash mismatch")
	if ClassifyExit(r) != ExitArchiveFailure {
		t.Fatal("recover error must map to exit 3")
	}
	if ClassifyExit(io.EOF) != ExitInvariantFailed {
		t.Fatal("plain error must map to exit 4")
	}
}
