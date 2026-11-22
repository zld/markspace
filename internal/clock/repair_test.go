package clock

import (
	"errors"
	"testing"

	"example.com/mediatimeline/internal/model"
)

func TestRepairWrap(t *testing.T) {
	// Anchor at the pre-wrap start so repaired times are relative to the track
	// origin. The wrap is still carried in the epoch.
	r, err := NewRepairer(model.TrackVideoMain, RepairOptions{Timescale: 90000, Modulus: Default32Modulus, AnchorTicks: Default32Modulus - 1000})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := r.Decode(Default32Modulus - 1000)
	if first.RawTicks != Default32Modulus-1000 {
		t.Fatalf("unexpected raw ticks %d", first.RawTicks)
	}
	// Crossing the wrap: -1000 + 2000 = forward 1000 ticks.
	second, err := r.Decode(1000)
	if err != nil {
		t.Fatalf("decode after wrap: %v", err)
	}
	if second.Epoch != 1 {
		t.Fatalf("epoch = %d, want 1", second.Epoch)
	}
	got, err := FromModel(second.RepairedTime)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := New(2000, 90000)
	if !got.Equal(want) {
		t.Fatalf("repaired = %s, want %s", got, want)
	}
	firstTime, _ := FromModel(first.RepairedTime)
	if !firstTime.IsZero() {
		t.Fatalf("first repaired time = %s, want 0 after anchoring", firstTime)
	}
	var sawWrap bool
	for _, d := range r.Decisions() {
		if d.Kind == DecisionWrap {
			sawWrap = true
		}
	}
	if !sawWrap {
		t.Fatal("wrap decision not recorded")
	}
}

func TestRepairBackwardUnrecoverable(t *testing.T) {
	r, err := NewRepairer(model.TrackAudioMain, RepairOptions{Timescale: 48000, Modulus: Default32Modulus})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Decode(48000); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Decode(96000); err != nil {
		t.Fatal(err)
	}
	ts, err := r.Decode(1000)
	if err == nil {
		t.Fatal("expected backward-step error")
	}
	var pe *model.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not *model.Error", err)
	}
	if pe.Track != model.TrackAudioMain || pe.Stage != model.StageClock || pe.Code != model.CodeBackwardTimestamp {
		t.Fatalf("unexpected classified error: %v", pe)
	}
	if pe.Recoverable {
		t.Fatal("backward step must be unrecoverable")
	}
	if ts.State != model.DecodeRejected {
		t.Fatalf("state = %s, want rejected", ts.State)
	}
}

func TestRepairReorderIsRecoverableAndSkipped(t *testing.T) {
	r, err := NewRepairer(model.TrackVideoMain, RepairOptions{Timescale: 90000, Modulus: Default32Modulus, JitterTicks: 3000})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []int64{0, 3000, 6000, 9000} {
		if _, err := r.Decode(v); err != nil {
			t.Fatalf("decode %d: %v", v, err)
		}
	}
	// 1500 ticks late is reordering. Reordered samples keep their true
	// timeline position and are forwarded, while the running maximum does
	// not advance past the highest timestamp seen.
	ts, err := r.Decode(7500)
	if err != nil {
		t.Fatalf("reordered timestamp should be recoverable: %v", err)
	}
	if ts.State != model.DecodeForwarded {
		t.Fatalf("state = %s, want forwarded", ts.State)
	}
	got, err := FromModel(ts.RepairedTime)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := New(7500, 90000); !got.Equal(want) {
		t.Fatalf("reordered repaired = %s, want %s", got, want)
	}
	// Next in-order sample continues from the running maximum (9000).
	next, err := r.Decode(12000)
	if err != nil {
		t.Fatal(err)
	}
	if nextTime, _ := FromModel(next.RepairedTime); !nextTime.Equal(mustNew(t, 12000, 90000)) {
		t.Fatalf("repaired = %s, want 12000/90000", nextTime)
	}
	if next.Epoch != 0 {
		t.Fatalf("reorder must not change the epoch, got %d", next.Epoch)
	}
}

func TestRepairLatePacketPreviousEpoch(t *testing.T) {
	r, err := NewRepairer(model.TrackVideoMain, RepairOptions{Timescale: 90000, Modulus: Default32Modulus, JitterTicks: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Decode(Default32Modulus - 9000); err != nil {
		t.Fatal(err)
	}
	ts, err := r.Decode(3000) // forward through the wrap
	if err != nil {
		t.Fatal(err)
	}
	if ts.Epoch != 1 {
		t.Fatalf("epoch = %d, want 1", ts.Epoch)
	}
	late, err := r.Decode(Default32Modulus - 6000)
	if err != nil {
		t.Fatalf("late packet should be recoverable: %v", err)
	}
	lateTime, err := FromModel(late.RepairedTime)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := New(Default32Modulus-6000, 90000)
	if !lateTime.Equal(want) {
		t.Fatalf("late repaired = %s, want %s", lateTime, want)
	}
	if late.State != model.DecodeForwarded {
		t.Fatalf("state = %s, want forwarded", late.State)
	}
}

func TestRepairAnchorShift(t *testing.T) {
	r, err := NewRepairer(model.TrackEmsg, RepairOptions{Timescale: 1000, Modulus: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Decode(5000); err != nil {
		t.Fatal(err)
	}
	r.SetAnchor(4000)
	ts, err := r.Decode(6000)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromModel(ts.RepairedTime)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := New(2, 1); !got.Equal(want) {
		t.Fatalf("repaired = %s, want 2s after anchoring", got)
	}
}

func TestCommonOrigin(t *testing.T) {
	origin, err := CommonOrigin([]Anchor{
		{Track: model.TrackVideoMain, FirstTicks: 9000, Timescale: 90000},
		{Track: model.TrackAudioMain, FirstTicks: 1000, Timescale: 48000},
	})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := New(1000, 48000)
	if !origin.Equal(want) {
		t.Fatalf("origin = %s, want %s", origin, want)
	}
}

func TestRepairerRejectsBadOptions(t *testing.T) {
	if _, err := NewRepairer(model.TrackVideoMain, RepairOptions{}); err == nil {
		t.Fatal("expected error for zero timescale")
	}
	if _, err := NewRepairer(model.TrackVideoMain, RepairOptions{Timescale: 1000, Modulus: -1}); err == nil {
		t.Fatal("expected error for negative modulus")
	}
	if _, err := NewRepairer(model.TrackVideoMain, RepairOptions{Timescale: 1000, Modulus: Default32Modulus}); err != nil {
		t.Fatal(err)
	}
	// A raw tick outside [0, modulus) cannot be interpreted.
	r, _ := NewRepairer(model.TrackVideoMain, RepairOptions{Timescale: 1000, Modulus: Default32Modulus})
	if _, err := r.Decode(Default32Modulus); err == nil {
		t.Fatal("expected clock-overflow error")
	}
}
