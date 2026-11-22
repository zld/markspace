package audio

import (
	"errors"
	"strings"
	"testing"

	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/model"
)

func TestLookupAndForChannels(t *testing.T) {
	if l, ok := Lookup("5.1"); !ok || len(l.Channels) != 6 || l.Channels[3] != "LFE" {
		t.Fatalf("5.1 = %+v, %v", l, ok)
	}
	if _, ok := Lookup("atmos-42"); ok {
		t.Fatal("unknown layout should not resolve")
	}
	if l, ok := ForChannels(2); !ok || l.Name != "stereo" {
		t.Fatalf("ForChannels(2) = %+v", l)
	}
	if _, ok := ForChannels(3); ok {
		t.Fatal("3 channels is ambiguous and must not resolve")
	}
}

func TestPlanIdentityAndRemap(t *testing.T) {
	stereo, _ := Lookup("stereo")
	m, err := Plan(stereo, stereo)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Source) != 2 || m.Source[0] != 0 || m.Source[1] != 1 {
		t.Fatalf("identity mapping = %+v", m)
	}
	five0, _ := Lookup("5.0")
	five1, _ := Lookup("5.1")
	// 5.1 -> 5.0 drops the LFE explicitly.
	m, err = Plan(five1, five0)
	if err != nil {
		t.Fatalf("5.1 -> 5.0 should map through explicit dropping: %v", err)
	}
	if len(m.Source) != 5 || m.Source[2] != 2 || m.Source[3] != 4 {
		t.Fatalf("5.1 -> 5.0 mapping = %+v", m)
	}
	// stereo has no centre channel: mapping to 5.1 must fail, not guess.
	if _, err := Plan(stereo, five1); err == nil {
		t.Fatal("stereo -> 5.1 must be rejected")
	}
	// 5.1(side) can map to 5.1 through the back/side counterpart, explicitly.
	side, _ := Lookup("5.1(side)")
	m, err = Plan(side, five1)
	if err != nil {
		t.Fatalf("side -> back remap should succeed: %v", err)
	}
	if !strings.Contains(m.Reason, "remapped") {
		t.Fatalf("remap must be recorded explicitly, got %q", m.Reason)
	}
}

func TestResolveRecordsConflicts(t *testing.T) {
	layout, mapping, errs, err := Resolve(model.TrackAudioMain, []model.AudioDescriptor{
		{Source: "manifest", Layout: "5.1"},
		{Source: "mp4-stsd", Layout: "stereo"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Sorted by source: mp4-stsd precedes manifest, so stereo wins.
	if layout.Name != "stereo" {
		t.Fatalf("selected %s, want stereo", layout.Name)
	}
	if mapping.To.Name != layout.Name {
		t.Fatalf("mapping target %s != %s", mapping.To.Name, layout.Name)
	}
	// The manifest declaration cannot map into stereo without inventing
	// channels; the conflict is recorded rather than dropped silently.
	found := false
	for _, e := range errs {
		if e.Code == model.CodeAudioLayout {
			found = true
		}
	}
	if !found {
		t.Fatalf("layout conflict not recorded: %v", errs)
	}
}

func TestResolveNoUsableLayoutIsUnrecoverable(t *testing.T) {
	// Three channels has no canonical layout, so no target can be named.
	_, _, _, err := Resolve(model.TrackAudioMain, []model.AudioDescriptor{
		{Source: "a", ChannelCount: 3},
	})
	if err == nil {
		t.Fatal("expected unrecoverable layout error")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Stage != model.StageAudio || pe.Recoverable {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestResolveIncompatibleMappingsAreRecorded(t *testing.T) {
	// Stereo wins by precedence; the 5.1 declaration cannot map into it, and
	// that fact must be recorded as a recoverable error rather than dropped.
	layout, _, errs, err := Resolve(model.TrackAudioMain, []model.AudioDescriptor{
		{Source: "mp4-stsd", Layout: "stereo"},
		{Source: "manifest", Layout: "5.1"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if layout.Name != "stereo" {
		t.Fatalf("selected %s, want stereo", layout.Name)
	}
	var recorded bool
	for _, e := range errs {
		if e.Code == model.CodeAudioLayout && strings.Contains(e.Message, "maps differently") {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("divergent mapping not recorded: %v", errs)
	}
}

func TestResolveRateConflictAndBoundary(t *testing.T) {
	d, errs, err := ResolveRate(model.TrackAudioMain, []uint32{44100, 48000})
	if err != nil {
		t.Fatalf("ResolveRate: %v", err)
	}
	if d.Selected != 48000 || !d.Resampled || d.Boundary == nil {
		t.Fatalf("decision = %+v", d)
	}
	r, err := clock.FromModel(*d.Boundary)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := clock.New(48000, 44100); !r.Equal(want) {
		t.Fatalf("boundary = %s, want 160/147", r)
	}
	if len(errs) != 1 || !errs[0].Recoverable {
		t.Fatalf("conflict should be one recoverable error, got %v", errs)
	}
}

func TestResolveRateRejectsUnsupported(t *testing.T) {
	_, _, err := ResolveRate(model.TrackAudioMain, []uint32{12345})
	if err == nil {
		t.Fatal("expected unsupported-rate error")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeAudioRate || pe.Recoverable {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestConvertTicksExact(t *testing.T) {
	got, err := ConvertTicks(44100, 44100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 48000 {
		t.Fatalf("ConvertTicks = %d, want 48000", got)
	}
	if _, err := ConvertTicks(1, 0, 48000); err == nil {
		t.Fatal("zero rate should fail")
	}
}

func frameAt(t *testing.T, ticks int64, size int) Frame {
	t.Helper()
	r, err := clock.FromTicks(ticks, 48000)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := clock.FromTicks(1024, 48000)
	return Frame{Index: int(ticks / 1024), Start: clock.ToModel(r), Duration: clock.ToModel(d), Size: size}
}

func TestReconcileGapConcealed(t *testing.T) {
	res, err := Reconcile([]Frame{frameAt(t, 0, 192), frameAt(t, 3*1024, 192)}, ReconcileOptions{
		Track: model.TrackAudioMain, FrameTicks: 1024, Timescale: 48000, MaxGapTicks: 48000, FullFrameBytes: 192,
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(res.Frames) != 3 {
		t.Fatalf("frames = %d, want 3 (one concealment)", len(res.Frames))
	}
	var kinds []DecisionKind
	for _, d := range res.Decisions {
		kinds = append(kinds, d.Kind)
	}
	if len(kinds) != 1 || kinds[0] != DecisionGapConcealed {
		t.Fatalf("decisions = %v, want gap-concealed", kinds)
	}
	for i := 1; i < len(res.Frames); i++ {
		a, _ := clock.FromModel(res.Frames[i-1].Start)
		b, _ := clock.FromModel(res.Frames[i].Start)
		if !a.Less(b) {
			t.Fatalf("frames are not monotonic at %d", i)
		}
	}
}

func TestReconcileGapOverflowUnrecoverable(t *testing.T) {
	_, err := Reconcile([]Frame{frameAt(t, 0, 192), frameAt(t, 100*1024, 192)}, ReconcileOptions{
		Track: model.TrackAudioMain, FrameTicks: 1024, Timescale: 48000, MaxGapTicks: 48000, FullFrameBytes: 192,
	})
	if err == nil {
		t.Fatal("expected gap overflow")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeAudioGapOverflow || pe.Recoverable {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestReconcileOverlapAndDuplicate(t *testing.T) {
	res, err := Reconcile([]Frame{
		frameAt(t, 0, 192),
		frameAt(t, 512, 192),  // half-frame overlap: trimmed
		frameAt(t, 1024, 192), // duplicate start: dropped
	}, ReconcileOptions{Track: model.TrackAudioMain, FrameTicks: 1024, Timescale: 48000, MaxGapTicks: 48000, FullFrameBytes: 192})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var trimmed, dropped bool
	for _, d := range res.Decisions {
		switch d.Kind {
		case DecisionOverlapTrim:
			trimmed = true
		case DecisionDuplicate:
			dropped = true
		}
	}
	if !trimmed || !dropped {
		t.Fatalf("expected overlap-trim and duplicate-drop, got %+v", res.Decisions)
	}
}

func TestReconcileShortTail(t *testing.T) {
	// A one-third frame tail is rejected; a 3/4 frame tail is accepted.
	_, err := Reconcile([]Frame{frameAt(t, 0, 192), {Index: 1, Start: mustTime(t, 1920), Duration: mustTime(t, 1024), Size: 40}}, ReconcileOptions{
		Track: model.TrackAudioMain, FrameTicks: 1024, Timescale: 48000, MaxGapTicks: 48000, FullFrameBytes: 192,
	})
	if err == nil {
		t.Fatal("expected the short tail to be rejected")
	}
	res, err := Reconcile([]Frame{frameAt(t, 0, 192), {Index: 1, Start: mustTime(t, 1024), Duration: mustTime(t, 512), Size: 160}}, ReconcileOptions{
		Track: model.TrackAudioMain, FrameTicks: 1024, Timescale: 48000, MaxGapTicks: 48000, FullFrameBytes: 192,
	})
	if err != nil {
		t.Fatalf("a 160/192-byte half-frame tail should be accepted: %v", err)
	}
	found := false
	for _, d := range res.Decisions {
		if d.Kind == DecisionShortTail {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected short-tail-accepted, got %+v", res.Decisions)
	}
}

func TestReconcileEmptyIsUnrecoverable(t *testing.T) {
	_, err := Reconcile(nil, ReconcileOptions{Track: model.TrackAudioMain, FrameTicks: 1024, Timescale: 48000, MaxGapTicks: 48000})
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *model.Error
	if !errors.As(err, &pe) || pe.Recoverable {
		t.Fatalf("empty audio must be an unrecoverable error: %v", err)
	}
}

func mustTime(t *testing.T, ticks int64) model.RationalTime {
	t.Helper()
	r, err := clock.FromTicks(ticks, 48000)
	if err != nil {
		t.Fatal(err)
	}
	return clock.ToModel(r)
}
