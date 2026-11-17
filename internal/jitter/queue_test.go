package jitter

import "testing"

func pcm(v int16) []int16 { return []int16{v, v, v} }

func TestSeqLessWrap(t *testing.T) {
	if !SeqLess(65534, 1) {
		t.Fatal("65534 should precede 1 across wrap")
	}
	if SeqLess(1, 65534) {
		t.Fatal("1 should not precede 65534")
	}
	if SeqLess(5, 5) {
		t.Fatal("equal sequences are not less")
	}
}

func TestQueueOrderedInsert(t *testing.T) {
	q := NewQueue(8)
	for _, s := range []uint16{65534, 0, 65535, 1} {
		if err := q.Insert(Frame{Seq: s, PCM: pcm(int16(s))}); err != nil {
			t.Fatalf("insert %d: %v", s, err)
		}
	}
	want := []uint16{65534, 65535, 0, 1}
	for i, w := range want {
		if q.frames[i].Seq != w {
			t.Fatalf("pos %d: seq %d, want %d", i, q.frames[i].Seq, w)
		}
	}
	f, ok := q.Pop()
	if !ok || f.Seq != 65534 {
		t.Fatalf("pop: %+v ok=%v", f, ok)
	}
}

func TestQueueDuplicateAndConflict(t *testing.T) {
	q := NewQueue(4)
	if err := q.Insert(Frame{Seq: 7, PCM: pcm(1)}); err != nil {
		t.Fatal(err)
	}
	if err := q.Insert(Frame{Seq: 7, PCM: pcm(1)}); err != ErrDuplicate {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
	if err := q.Insert(Frame{Seq: 7, PCM: pcm(2)}); err != ErrConflict {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	if q.Len() != 1 {
		t.Fatalf("len=%d, want 1", q.Len())
	}
}

func TestQueueFullAndDropBefore(t *testing.T) {
	q := NewQueue(3)
	for s := uint16(10); s < 13; s++ {
		if err := q.Insert(Frame{Seq: s, PCM: pcm(0)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Insert(Frame{Seq: 13, PCM: pcm(0)}); err != ErrFull {
		t.Fatalf("want ErrFull, got %v", err)
	}
	if n := q.DropBefore(12); n != 2 {
		t.Fatalf("dropped %d, want 2", n)
	}
	if err := q.Insert(Frame{Seq: 13, PCM: pcm(0)}); err != nil {
		t.Fatalf("insert after eviction: %v", err)
	}
}
