package repair

import (
	"testing"

	"archive-recovery/internal/media"
)

func newTestRepairer(t *testing.T) *TimestampRepairer {
	t.Helper()
	clock, err := media.NewClock(90000, 1)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewTimestampRepairer(clock, 3000) // 30 fps on a 90 kHz clock
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func frame(i int, pts int64, has bool) *media.FrameEvent {
	return &media.FrameEvent{Index: i, RawPTS: pts, HasPTS: has, IsFrame: true}
}

func collect(t *testing.T, r *TimestampRepairer, evs ...*media.FrameEvent) []Decision {
	t.Helper()
	out := make([]Decision, 0, len(evs))
	prev := int64(-1)
	for _, ev := range evs {
		d := r.Repair(ev)
		if d.FixedPTS <= prev {
			t.Fatalf("non-monotonic output at frame %d: %d after %d", ev.Index, d.FixedPTS, prev)
		}
		prev = d.FixedPTS
		out = append(out, d)
	}
	return out
}

func TestMissingFirstAndMiddle(t *testing.T) {
	r := newTestRepairer(t)
	ds := collect(t, r,
		frame(0, 0, false),
		frame(1, 3000, true),
		frame(2, 0, false),
	)
	if ds[0].Reason != ReasonMissing || ds[0].FixedPTS != 0 {
		t.Fatalf("first missing: %+v", ds[0])
	}
	if ds[1].Reason != ReasonOK || ds[1].FixedPTS != 3000 {
		t.Fatalf("second ok: %+v", ds[1])
	}
	if ds[2].Reason != ReasonMissing || ds[2].FixedPTS != 6000 {
		t.Fatalf("interpolated missing: %+v", ds[2])
	}
}

func TestDuplicateAndRegress(t *testing.T) {
	r := newTestRepairer(t)
	ds := collect(t, r,
		frame(0, 0, true),
		frame(1, 3000, true),
		frame(2, 3000, true), // duplicate
		frame(3, 2000, true), // regressive
		frame(4, 12000, true),
	)
	if ds[2].Reason != ReasonDuplicate || ds[2].FixedPTS != 6000 {
		t.Fatalf("duplicate handling: %+v", ds[2])
	}
	if ds[3].Reason != ReasonRegress || ds[3].FixedPTS != 9000 {
		t.Fatalf("regression handling: %+v", ds[3])
	}
	if ds[4].Reason != ReasonOK || ds[4].FixedPTS != 12000 {
		t.Fatalf("resumed valid stream should stay raw: %+v", ds[4])
	}
}

func TestWrap33(t *testing.T) {
	r := newTestRepairer(t)
	high := wrapMask33 - 100
	ds := collect(t, r,
		frame(0, high, true),
		frame(1, 50, true),   // wraps: 50 + 2^33
		frame(2, 3050, true), // stays unwrapped on second cycle
	)
	if ds[1].Reason != ReasonWrap33 {
		t.Fatalf("wrap reason: %+v", ds[1])
	}
	if ds[1].FixedPTS != 50+wrapMod33 || ds[1].FixedPTS <= ds[0].FixedPTS {
		t.Fatalf("wrapped tick wrong: %+v", ds[1])
	}
	if ds[2].Reason != ReasonOK || ds[2].FixedPTS != 3050+wrapMod33 {
		t.Fatalf("post-wrap continuity wrong: %+v", ds[2])
	}
}

func TestAllMissingAreMonotonic(t *testing.T) {
	r := newTestRepairer(t)
	evs := make([]*media.FrameEvent, 5)
	for i := range evs {
		evs[i] = frame(i, 0, false)
	}
	ds := collect(t, r, evs...)
	for i, d := range ds {
		if d.Reason != ReasonMissing || d.FixedPTS != int64(i)*3000 {
			t.Fatalf("frame %d: %+v", i, d)
		}
	}
}

func TestInvalidStepRejected(t *testing.T) {
	clock, _ := media.NewClock(1, 1)
	if _, err := NewTimestampRepairer(clock, 0); media.ClassOf(err) != media.ClassInvalidArgument {
		t.Fatalf("zero tick step must be ClassInvalidArgument, got %v", err)
	}
}
