package timeline

import (
	"reflect"
	"testing"

	"mediaforge/internal/clock"
)

func TestWatermarkAdvancesByDelay(t *testing.T) {
	wm := NewWatermark(clock.NewRat(960, 48000)) // 20 ms
	wm.Observe(clock.NewTime(0, 48000))
	if h, _ := wm.Horizon(); h.Num != 0 {
		t.Fatalf("horizon = %v want 0", h)
	}
	wm.Observe(clock.NewTime(48000, 48000))
	if h, _ := wm.Horizon(); h.Cmp(clock.NewRat(48000-960, 48000)) != 0 {
		t.Fatalf("horizon = %v", h)
	}
	if !wm.IsLate(clock.NewTime(0, 48000)) {
		t.Fatal("expected early packet to be late")
	}
	if wm.IsLate(clock.NewTime(48000-960, 48000)) {
		t.Fatal("horizon is inclusive")
	}
}

func TestManagerDedupReorderAndLoss(t *testing.T) {
	m := NewManager(48000, 960)
	// Out of order arrival: 10, 12, 11, and a duplicate of 10.
	m.Push(10, 0, []byte{1}, 960)
	m.Push(12, 1920, []byte{3}, 960)
	m.Push(11, 960, []byte{2}, 960)
	m.Push(10, 0, []byte{1}, 960)
	s := m.Summary()
	if s.Unique != 3 || s.Duplicates != 1 || s.Loss != 0 {
		t.Fatalf("summary = %+v", s)
	}
	var seqs []uint16
	for _, e := range s.Events {
		seqs = append(seqs, e.Seq)
	}
	if !reflect.DeepEqual(seqs, []uint16{10, 11, 12}) {
		t.Fatalf("order = %v", seqs)
	}
}

func TestManagerLossAndGaps(t *testing.T) {
	m := NewManager(48000, 0)
	for seq := uint16(20); seq <= 24; seq++ {
		m.Push(seq, uint32(seq-20)*960, nil, 960)
	}
	m.Push(27, 7*960, nil, 960)
	s := m.Summary()
	if s.Loss != 2 {
		t.Fatalf("loss = %d", s.Loss)
	}
	want := []Gap{{StartSeq: 5, EndSeq: 7}}
	if !reflect.DeepEqual(s.Gaps, want) {
		t.Fatalf("gaps = %+v", s.Gaps)
	}
}

func TestManagerSequenceWrap(t *testing.T) {
	m := NewManager(48000, 0)
	for _, seq := range []uint16{65534, 65535, 0, 1} {
		m.Push(seq, uint32(seq)*960, nil, 960)
	}
	s := m.Summary()
	if s.Unique != 4 || s.Loss != 0 || len(s.Gaps) != 0 {
		t.Fatalf("summary = %+v", s)
	}
	if s.Events[3].ExtSeq != 3 {
		t.Fatalf("extseq = %d", s.Events[3].ExtSeq)
	}
}

func TestManagerLateReplayIsDeterministic(t *testing.T) {
	run := func() Summary {
		m := NewManager(48000, 480)
		m.Push(1, 0, nil, 960)
		m.Push(3, 1920, nil, 960)
		m.Push(2, 960, nil, 960) // late: horizon is 1920-480 = 1440 > 960
		return m.Summary()
	}
	a, b := run(), run()
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("non-deterministic summary:\n%+v\n%+v", a, b)
	}
	if a.Late != 1 {
		t.Fatalf("late = %d", a.Late)
	}
	if a.Events[1].AffectedStart != 960 || a.Events[1].AffectedEnd != 1920 {
		t.Fatalf("affected = [%d,%d)", a.Events[1].AffectedStart, a.Events[1].AffectedEnd)
	}
	if got := a.Events[1].AffectedEnd - a.Events[1].AffectedStart; got != 960 {
		t.Fatalf("replay window should equal payload samples, got %d", got)
	}
}

func TestManagerTimestampWrap(t *testing.T) {
	m := NewManager(48000, 0)
	m.Push(1, 0xFFFFFE00, nil, 512)
	m.Push(2, 0x00000000, nil, 512)
	s := m.Summary()
	if s.Events[1].ExtTS != 512 {
		t.Fatalf("ext ts = %d", s.Events[1].ExtTS)
	}
}

func BenchmarkPush(b *testing.B) {
	m := NewManager(48000, 0)
	for i := 0; i < b.N; i++ {
		m.Push(uint16(i), uint32(i)*960, nil, 960)
	}
}
