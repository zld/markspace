package timeline

import (
	"errors"
	"testing"

	"mediapipe/internal/model"
)

func mustRate(t *testing.T, n, d int64) model.Rational {
	t.Helper()
	r, err := model.NewRational(n, d)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIdentityEditMapsDirectly(t *testing.T) {
	tr := Track{
		Asset: "a", Version: "v", TrackID: "1", Kind: "video", Timescale: 90_000,
		Segments: []Segment{
			{StartTicks: 0, DurationTicks: 90_000, Summary: "s0"},
			{StartTicks: 90_000, DurationTicks: 90_000, Summary: "s1"},
		},
	}
	mapped, err := MapVersion("a", "v", []Track{tr})
	if err != nil {
		t.Fatal(err)
	}
	m := mapped[0]
	if m.Start() != 0 {
		t.Fatalf("start = %v", m.Start())
	}
	if m.End() != model.Nanos(2*model.NanosPerSecond) {
		t.Fatalf("end = %v, want 2s", m.End())
	}
	if len(m.Segments) != 2 || m.Segments[1].Timeline.Start != model.Nanos(model.NanosPerSecond) {
		t.Fatalf("segments = %+v", m.Segments)
	}
}

func TestEditTrimsLeadingMedia(t *testing.T) {
	// Edit starts 1s into the media; the first second is trimmed.
	tr := Track{
		Asset: "a", Version: "v", TrackID: "1", Kind: "video", Timescale: 24,
		Edits: EditList{{MediaTime: 24, DurationTicks: 48, Rate: mustRate(t, 1, 1)}},
		Segments: []Segment{
			{StartTicks: 0, DurationTicks: 24, Summary: "trimmed"},
			{StartTicks: 24, DurationTicks: 24, Summary: "kept-a"},
			{StartTicks: 48, DurationTicks: 24, Summary: "kept-b"},
		},
	}
	mapped, err := MapVersion("a", "v", []Track{tr})
	if err != nil {
		t.Fatal(err)
	}
	m := mapped[0]
	if len(m.Segments) != 2 {
		t.Fatalf("segments = %+v", m.Segments)
	}
	if m.Segments[0].Summary != "kept-a" || m.Segments[0].Timeline.Start != 0 {
		t.Fatalf("first = %+v", m.Segments[0])
	}
	if m.Segments[0].MediaTick != 24 {
		t.Fatalf("raw media tick not preserved: %d", m.Segments[0].MediaTick)
	}
	if m.Segments[1].Timeline.Start != model.Nanos(model.NanosPerSecond) {
		t.Fatalf("second start = %v", m.Segments[1].Timeline.Start)
	}
}

func TestEmptyEditShiftsPresentation(t *testing.T) {
	// 500ms of empty edit before the media: presentation starts at 0.5s.
	tr := Track{
		Asset: "a", Version: "v", TrackID: "1", Kind: "audio", Timescale: 1000,
		Edits: EditList{
			{MediaTime: -1, DurationTicks: 500, Rate: mustRate(t, 1, 1)},
			{MediaTime: 0, DurationTicks: 1000, Rate: mustRate(t, 1, 1)},
		},
		Segments: []Segment{{StartTicks: 0, DurationTicks: 1000, Summary: "s"}},
	}
	mapped, err := MapVersion("a", "v", []Track{tr})
	if err != nil {
		t.Fatal(err)
	}
	m := mapped[0]
	// Origin normalisation subtracts the earliest presented start, which is
	// this track's own 500ms, so the normalised timeline starts at 0 while
	// the absolute origin is retained.
	if m.PresentedOrigin != 500*model.Nanos(1_000_000) {
		t.Fatalf("presented origin = %v", m.PresentedOrigin)
	}
	if m.Start() != 0 || m.End() != model.Nanos(model.NanosPerSecond) {
		t.Fatalf("normalised span = [%v,%v]", m.Start(), m.End())
	}
}

func TestNonZeroStartAndCommonBase(t *testing.T) {
	// Video media starts at 3s, audio at 3s + 20ms; both are edited to
	// present from the same point, so the normalised starts must agree.
	video := Track{
		Asset: "a", Version: "v", TrackID: "1", Kind: "video", Timescale: 90_000,
		Edits:    EditList{{MediaTime: 3 * 90_000, DurationTicks: 0, Rate: mustRate(t, 1, 1)}},
		Segments: []Segment{{StartTicks: 3 * 90_000, DurationTicks: 180_000, Summary: "v"}},
	}
	audio := Track{
		Asset: "a", Version: "v", TrackID: "2", Kind: "audio", Timescale: 48_000,
		Edits:    EditList{{MediaTime: 3 * 48_000, DurationTicks: 0, Rate: mustRate(t, 1, 1)}},
		Segments: []Segment{{StartTicks: 3 * 48_000, DurationTicks: 96_000, Summary: "a"}},
	}
	mapped, err := MapVersion("a", "v", []Track{video, audio})
	if err != nil {
		t.Fatal(err)
	}
	if mapped[0].Start() != 0 || mapped[1].Start() != 0 {
		t.Fatalf("starts = %v %v", mapped[0].Start(), mapped[1].Start())
	}
	rep := SyncError(mapped)
	if rep.ErrorMS != 0 {
		t.Fatalf("sync error = %dms, want 0", rep.ErrorMS)
	}
	if mapped[0].PresentedOrigin != mapped[1].PresentedOrigin {
		t.Fatalf("origins differ: %v %v", mapped[0].PresentedOrigin, mapped[1].PresentedOrigin)
	}
}

func TestOriginNormalisationTracksSkew(t *testing.T) {
	// Audio presented origin is 30 ms later than video; the version origin
	// becomes video's origin and the audio track is shifted back, so the
	// residual boundary skew is 30 ms and must be reported.
	video := Track{
		Asset: "a", Version: "v", TrackID: "1", Kind: "video", Timescale: 1000,
		Segments: []Segment{{StartTicks: 0, DurationTicks: 2000, Summary: "v"}},
	}
	audio := Track{
		Asset: "a", Version: "v", TrackID: "2", Kind: "audio", Timescale: 1000,
		Segments: []Segment{{StartTicks: 30, DurationTicks: 2000, Summary: "a"}},
	}
	mapped, err := MapVersion("a", "v", []Track{video, audio})
	if err != nil {
		t.Fatal(err)
	}
	// Both tracks subtract the same version origin (video's 0).
	if mapped[0].OriginShift != 0 || mapped[1].OriginShift != 0 {
		t.Fatalf("shifts = %v %v", mapped[0].OriginShift, mapped[1].OriginShift)
	}
	// The audio track keeps its 30 ms residual offset from the common base.
	if mapped[1].Start() != 30*model.Nanos(1_000_000) {
		t.Fatalf("audio start = %v, want 30ms", mapped[1].Start())
	}
	rep := SyncError(mapped)
	if rep.ErrorMS != 30 {
		t.Fatalf("sync error = %dms, want 30", rep.ErrorMS)
	}
}

func TestSegmentSplitByEmptyEdit(t *testing.T) {
	// A 400-tick segment is split by an empty edit: media [0,200) then
	// empty 100 ticks then media [200,400).
	tr := Track{
		Asset: "a", Version: "v", TrackID: "1", Kind: "video", Timescale: 1000,
		Edits: EditList{
			{MediaTime: 0, DurationTicks: 200, Rate: mustRate(t, 1, 1)},
			{MediaTime: -1, DurationTicks: 100, Rate: mustRate(t, 1, 1)},
			{MediaTime: 200, DurationTicks: 200, Rate: mustRate(t, 1, 1)},
		},
		Segments: []Segment{{StartTicks: 0, DurationTicks: 400, Summary: "s"}},
	}
	mapped, err := MapVersion("a", "v", []Track{tr})
	if err != nil {
		t.Fatal(err)
	}
	m := mapped[0]
	if len(m.Segments) != 2 {
		t.Fatalf("segments = %+v", m.Segments)
	}
	if got := m.Segments[0].Timeline; got.Start != 0 || got.End != 200*model.Nanos(1_000_000) {
		t.Fatalf("first span = %v", got)
	}
	if got := m.Segments[1].Timeline; got.Start != 300*model.Nanos(1_000_000) || got.End != 500*model.Nanos(1_000_000) {
		t.Fatalf("second span = %v", got)
	}
}

func TestEditRateScaling(t *testing.T) {
	// Half-speed edit: 1s of media is presented over 2s.
	tr := Track{
		Asset: "a", Version: "v", TrackID: "1", Kind: "video", Timescale: 1000,
		Edits:    EditList{{MediaTime: 0, DurationTicks: 2000, Rate: mustRate(t, 1, 2)}},
		Segments: []Segment{{StartTicks: 0, DurationTicks: 1000, Summary: "s"}},
	}
	mapped, err := MapVersion("a", "v", []Track{tr})
	if err != nil {
		t.Fatal(err)
	}
	if got := mapped[0].End(); got != model.Nanos(2*model.NanosPerSecond) {
		t.Fatalf("end = %v, want 2s", got)
	}
}

func TestBadEditRateRejected(t *testing.T) {
	tr := Track{
		Asset: "a", Version: "v", TrackID: "1", Timescale: 1000,
		Edits:    EditList{{MediaTime: 0, DurationTicks: 100, Rate: model.Rational{Num: 0, Den: 1}}},
		Segments: []Segment{{StartTicks: 0, DurationTicks: 100, Summary: "s"}},
	}
	_, err := MapVersion("a", "v", []Track{tr})
	if err == nil {
		t.Fatal("expected bad-rate error")
	}
	var me *model.Error
	if !errors.As(err, &me) || me.Stage != model.StageTimeline {
		t.Fatalf("error = %v", err)
	}
}

func TestAllSegmentsTrimmedIsError(t *testing.T) {
	tr := Track{
		Asset: "a", Version: "v", TrackID: "1", Timescale: 1000,
		Edits:    EditList{{MediaTime: 5000, DurationTicks: 100, Rate: mustRate(t, 1, 1)}},
		Segments: []Segment{{StartTicks: 0, DurationTicks: 100, Summary: "s"}},
	}
	_, err := MapVersion("a", "v", []Track{tr})
	if err == nil {
		t.Fatal("expected error when everything is trimmed")
	}
}

func TestLocate(t *testing.T) {
	tr := Track{
		Asset: "a", Version: "v", TrackID: "1", Timescale: 1000,
		Segments: []Segment{
			{StartTicks: 0, DurationTicks: 500, Summary: "a"},
			{StartTicks: 500, DurationTicks: 500, Summary: "b"},
		},
	}
	mapped, err := MapVersion("a", "v", []Track{tr})
	if err != nil {
		t.Fatal(err)
	}
	seg, dev, ok := mapped[0].Locate(700 * model.Nanos(1_000_000))
	if !ok || seg.Summary != "b" || dev != 200*model.Nanos(1_000_000) {
		t.Fatalf("locate = %+v dev=%v ok=%v", seg, dev, ok)
	}
	if _, _, ok := mapped[0].Locate(5000 * model.Nanos(1_000_000)); ok {
		t.Fatal("expected miss past end")
	}
}

func TestRTPClockWrap(t *testing.T) {
	c := RTPClock{ClockRate: 90_000, BaseRTP: 0xFFFF_FF00, BaseTime: 0}
	near, err := c.At(0xFFFF_FFFF)
	if err != nil {
		t.Fatal(err)
	}
	wantNear, err := model.NanosFromRatio(255, 90_000)
	if err != nil {
		t.Fatal(err)
	}
	if near != wantNear {
		t.Fatalf("near = %v, want %v", near, wantNear)
	}
	wrapped, err := c.At(0x0000_00FF)
	if err != nil {
		t.Fatal(err)
	}
	wantWrapped, err := model.NanosFromRatio(511, 90_000)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped != wantWrapped {
		t.Fatalf("wrapped = %v, want %v", wrapped, wantWrapped)
	}
	// 255 + 256 = 511 ticks across the 32-bit wrap boundary.
	if wrapped-near != wantWrapped-wantNear {
		t.Fatalf("wrap discontinuity: %v -> %v", near, wrapped)
	}
}

func TestNoFloatingDriftAcrossManySegments(t *testing.T) {
	// 1000 segments of 1ms each across 90kHz must land exactly on 1s.
	tr := Track{Asset: "a", Version: "v", TrackID: "1", Timescale: 90_000}
	for i := 0; i < 1000; i++ {
		tr.Segments = append(tr.Segments, Segment{
			StartTicks: int64(i) * 90, DurationTicks: 90, Summary: "s",
		})
	}
	mapped, err := MapVersion("a", "v", []Track{tr})
	if err != nil {
		t.Fatal(err)
	}
	if mapped[0].End() != model.Nanos(model.NanosPerSecond) {
		t.Fatalf("end = %v, want exactly 1s", mapped[0].End())
	}
}
