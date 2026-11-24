package timeline

import (
	"testing"

	"archive/internal/clock"
	"archive/internal/normalize"
	"archive/internal/probe"
)

func fixed(src *probe.Track, clips ...probe.ClipRef) (*probe.Track, *normalize.Resolution) {
	src.Clips = clips
	r, _ := normalize.Resolve(src)
	return src, r
}

func videoTrack(id string) *probe.Track {
	return &probe.Track{ID: id, Kind: "video", StoredWidth: 1920, StoredHeight: 1080,
		Timescale: 25, SampleIntervalNS: 40000000, DurationNS: 400000000}
}

func TestComposePlacesClipsOnSharedTimeline(t *testing.T) {
	a, ra := fixed(videoTrack("a"), probe.ClipRef{StartNS: 0, DurationNS: 200000000})
	b, rb := fixed(videoTrack("b"), probe.ClipRef{StartNS: 200000000, DurationNS: 200000000})
	res, err := Compose([]*probe.Track{a, b}, []*normalize.Resolution{ra, rb})
	if err != nil {
		t.Fatal(err)
	}
	if res.Timeline.DurationNS != 400000000 {
		t.Fatalf("duration = %d", res.Timeline.DurationNS)
	}
	for _, tr := range res.Timeline.Tracks {
		if tr.FrameCount() != 5 {
			t.Fatalf("%s frames = %d", tr.ID, tr.FrameCount())
		}
	}
}

func TestComposeSkipsNonMonotonicTrackAndKeepsOthers(t *testing.T) {
	bad := videoTrack("bad")
	bad.Clips = []probe.ClipRef{{StartNS: 200000000, DurationNS: 100000000}, {StartNS: 100000000, DurationNS: 100000000}}
	rb, _ := normalize.Resolve(bad)
	good, rg := fixed(videoTrack("good"), probe.ClipRef{StartNS: 0, DurationNS: 400000000})
	res, err := Compose([]*probe.Track{bad, good}, []*normalize.Resolution{rb, rg})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].TrackID != "bad" {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
	if clock.AsKind(res.Skipped[0].Reason) != clock.KindNonMonotonic {
		t.Fatalf("kind = %q", clock.AsKind(res.Skipped[0].Reason))
	}
	if len(res.Timeline.Tracks) != 1 || res.Timeline.Tracks[0].ID != "good" {
		t.Fatalf("tracks = %+v", res.Timeline.Tracks)
	}
}

func TestComposeNegativeDurationIsTyped(t *testing.T) {
	tr := videoTrack("neg")
	tr.DurationNS = -1
	r, _ := normalize.Resolve(tr)
	_, err := composeTrack(tr, r)
	if clock.AsKind(err) != clock.KindNegativeDuration {
		t.Fatalf("kind = %q err=%v", clock.AsKind(err), err)
	}
}

func TestComposeClipBeyondSourceIsOutOfRange(t *testing.T) {
	tr := videoTrack("over")
	tr.Clips = []probe.ClipRef{{StartNS: 0, DurationNS: 800000000}}
	r, _ := normalize.Resolve(tr)
	_, err := composeTrack(tr, r)
	if clock.AsKind(err) != clock.KindOutOfRange {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestAllInvalidTracksFailComposition(t *testing.T) {
	bad := videoTrack("bad")
	bad.Clips = []probe.ClipRef{{StartNS: -5, DurationNS: 400000000}}
	r, _ := normalize.Resolve(bad)
	_, err := Compose([]*probe.Track{bad}, []*normalize.Resolution{r})
	if err == nil {
		t.Fatal("expected composition failure when no track survives")
	}
}

func TestBoundaryErrorExactlyOnGrid(t *testing.T) {
	if got := BoundaryError(3*40000000, 40000000); got != 0 {
		t.Fatalf("boundary error = %d, want 0", got)
	}
	if got := BoundaryError(40000001, 40000000); got != 1 {
		t.Fatalf("boundary error = %d, want 1", got)
	}
}

func TestFrameTimesAreDeterministic(t *testing.T) {
	tr := &Track{ID: "x", FrameIntervalNS: 40000000}
	times := FrameTimes(tr, Clip{StartNS: 80000000, FrameCount: 3})
	want := []int64{80000000, 120000000, 160000000}
	for i := range want {
		if times[i] != want[i] {
			t.Fatalf("frame %d = %d want %d", i, times[i], want[i])
		}
	}
}
