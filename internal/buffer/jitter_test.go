package buffer

import (
	"testing"

	"mediad/internal/model"
)

func e(ms int64, seq uint64) *model.Event {
	return &model.Event{EventMs: ms, Seq: seq}
}

func TestJitterOrdersByEventTimeThenSeq(t *testing.T) {
	j := NewJitter(100)
	j.Push(e(300, 3))
	j.Push(e(100, 1))
	j.Push(e(100, 2))
	if j.Len() != 3 {
		t.Fatalf("len = %d", j.Len())
	}
	out := j.PopReady(1000)
	want := []int64{100, 100, 300}
	if len(out) != len(want) {
		t.Fatalf("popped %d, want %d", len(out), len(want))
	}
	for i := range want {
		if out[i].EventMs != want[i] {
			t.Fatalf("out[%d] ms = %d, want %d", i, out[i].EventMs, want[i])
		}
	}
	if out[0].Seq != 1 || out[1].Seq != 2 {
		t.Fatalf("equal-time events not ordered by seq: %d,%d", out[0].Seq, out[1].Seq)
	}
}

func TestJitterRespectsDepth(t *testing.T) {
	j := NewJitter(100)
	j.Push(e(400, 1)) // needs watermark >= 500
	j.Push(e(300, 2)) // needs watermark >= 400
	if got := j.PopReady(450); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("PopReady(450) = %+v", got)
	}
	if got := j.PopReady(499); len(got) != 0 {
		t.Fatalf("PopReady(499) released %d events early", len(got))
	}
	if got := j.PopReady(500); len(got) != 1 || got[0].Seq != 1 {
		t.Fatalf("PopReady(500) = %+v", got)
	}
}

func TestJitterFlushDrains(t *testing.T) {
	j := NewJitter(1000)
	j.Push(e(0, 1))
	j.Push(e(5000, 2))
	out := j.Flush()
	if len(out) != 2 || j.Len() != 0 {
		t.Fatalf("flush = %d, remaining %d", len(out), j.Len())
	}
	if out[0].Seq != 1 || out[1].Seq != 2 {
		t.Fatalf("flush order = %d,%d", out[0].Seq, out[1].Seq)
	}
}
