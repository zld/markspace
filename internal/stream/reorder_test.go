package stream

import (
	"reflect"
	"testing"

	"mediapipe/internal/model"
)

func pk(seq uint64) model.Packet {
	return model.Packet{ExtSequence: seq, Sequence: uint16(seq)}
}

func sequences(ps []model.Packet) []uint64 {
	out := make([]uint64, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ExtSequence)
	}
	return out
}

func TestReorderSortsWithinWindow(t *testing.T) {
	h := &Health{}
	r := NewReorder(8, h)
	var got []uint64
	got = append(got, sequences(r.Push(pk(10)))...)
	got = append(got, sequences(r.Push(pk(12)))...)
	got = append(got, sequences(r.Push(pk(11)))...)
	got = append(got, sequences(r.Push(pk(13)))...)
	got = append(got, sequences(r.Flush())...)
	if want := []uint64{10, 11, 12, 13}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if h.PacketLost != 0 || h.PacketLate != 0 || h.PacketDuplicate != 0 {
		t.Fatalf("unexpected health: %+v", h)
	}
}

func TestReorderWindowBoundaryMarksLossThenLate(t *testing.T) {
	h := &Health{}
	r := NewReorder(4, h)
	r.Push(pk(10)) // released, next=11
	r.Push(pk(12)) // buffered
	// seq 15 makes highest-next = 15-11 = 4: exactly at the window boundary.
	ready := r.Push(pk(15))
	if got := sequences(ready); !reflect.DeepEqual(got, []uint64{12}) {
		t.Fatalf("boundary ready = %v", got)
	}
	if h.PacketLost != 1 {
		t.Fatalf("lost = %d, want 1", h.PacketLost)
	}

	// The missing packet now arrives after the window and must not be emitted.
	if ready := r.Push(pk(11)); ready != nil {
		t.Fatalf("late packet emitted: %v", ready)
	}
	if h.PacketLate != 1 {
		t.Fatalf("late = %d, want 1", h.PacketLate)
	}
	if got := sequences(r.Flush()); !reflect.DeepEqual(got, []uint64{15}) {
		t.Fatalf("flush after late = %v, want [15]", got)
	}
}

func TestReorderDuplicatesDoNotReenter(t *testing.T) {
	h := &Health{}
	r := NewReorder(8, h)
	var got []uint64
	got = append(got, sequences(r.Push(pk(100)))...)
	r.Push(pk(102))
	got = append(got, sequences(r.Push(pk(101)))...)
	r.Push(pk(100)) // released duplicate
	r.Push(pk(102)) // released duplicate after drain
	got = append(got, sequences(r.Flush())...)
	if want := []uint64{100, 101, 102}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if h.PacketDuplicate != 2 {
		t.Fatalf("duplicates = %d, want 2", h.PacketDuplicate)
	}
}

func TestReorderFlushCountsObservedGapOnly(t *testing.T) {
	h := &Health{}
	r := NewReorder(64, h)
	got := sequences(r.Push(pk(1)))
	r.Push(pk(4))
	got = append(got, sequences(r.Flush())...)
	if want := []uint64{1, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if h.PacketLost != 2 {
		t.Fatalf("lost = %d, want sequences 2 and 3", h.PacketLost)
	}
}
