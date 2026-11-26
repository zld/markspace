package clock

import (
	"testing"

	"mediarc/internal/model"
)

func TestRepairWrap(t *testing.T) {
	base := uint32(1<<32 - 3000)
	in := []Sample{
		{SrcOffset: 0, Raw: base},
		{SrcOffset: 36, Raw: 0},
		{SrcOffset: 72, Raw: 3000},
	}
	got := Repair(in)
	want := []int64{1<<32 - 3000, 1 << 32, 1<<32 + 3000}
	for i, w := range want {
		if got[i].Ticks != w {
			t.Fatalf("ticks[%d] = %d, want %d", i, got[i].Ticks, w)
		}
		if got[i].Delta != got[i].Ticks-int64(got[i].Raw) {
			t.Fatalf("delta[%d] inconsistent", i)
		}
	}
	if got[1].Event != model.ErrWrap {
		t.Fatalf("event[1] = %q, want CLOCK_WRAP", got[1].Event)
	}
	if got[0].Event != "" || got[2].Event != "" {
		t.Fatalf("unexpected events: %q %q", got[0].Event, got[2].Event)
	}
}

func TestRepairSourceSwitch(t *testing.T) {
	in := []Sample{
		{SrcOffset: 0, Raw: 0},
		{SrcOffset: 36, Raw: 3000},
		{SrcOffset: 72, Raw: 100}, // new source, clock jumps back
		{SrcOffset: 108, Raw: 200},
	}
	got := Repair(in)
	want := []int64{0, 3000, 3001, 3101}
	for i, w := range want {
		if got[i].Ticks != w {
			t.Fatalf("ticks[%d] = %d, want %d", i, got[i].Ticks, w)
		}
	}
	if got[2].Event != model.ErrSourceSwitch {
		t.Fatalf("event[2] = %q, want SOURCE_SWITCH", got[2].Event)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Ticks <= got[i-1].Ticks {
			t.Fatalf("not monotonic at %d", i)
		}
	}
}

func TestRepairEmpty(t *testing.T) {
	if got := Repair(nil); len(got) != 0 {
		t.Fatalf("got %d, want 0", len(got))
	}
}
