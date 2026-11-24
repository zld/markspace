package timeline

import (
	"testing"

	"archive/internal/clock"
	"archive/internal/normalize"
	"archive/internal/probe"
)

func TestClipStartingNegativeIsOutOfRange(t *testing.T) {
	tr := videoTrack("neg-start")
	tr.Clips = []probe.ClipRef{{StartNS: -1, DurationNS: 40000000}}
	r, _ := normalize.Resolve(tr)
	_, err := composeTrack(tr, r)
	if clock.AsKind(err) != clock.KindOutOfRange {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestZeroDurationClipIsAllowedButEmptyTrackFails(t *testing.T) {
	tr := videoTrack("empty")
	tr.DurationNS = 0
	tr.Clips = []probe.ClipRef{{StartNS: 0, DurationNS: 0}}
	r, _ := normalize.Resolve(tr)
	_, err := composeTrack(tr, r)
	if clock.AsKind(err) != clock.KindInvalidMetadata {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestNonPositiveFrameIntervalRejected(t *testing.T) {
	tr := videoTrack("bad-interval")
	tr.SampleIntervalNS = 0
	r, _ := normalize.Resolve(tr)
	_, err := composeTrack(tr, r)
	if clock.AsKind(err) != clock.KindInvalidMetadata {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestMismatchedInputsRejected(t *testing.T) {
	_, err := Compose([]*probe.Track{videoTrack("a")}, nil)
	if clock.AsKind(err) != clock.KindInvalidMetadata {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}
