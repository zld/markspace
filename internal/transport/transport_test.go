package transport

import (
	"testing"

	"github.com/pion/rtp"
)

func mkRTP(t *testing.T, seq uint16, ts uint32) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 42},
		Payload: []byte{1, 2, 3, 4},
	}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestUnwrapWrapAround(t *testing.T) {
	u := &Unwrapper{}
	seqs := []uint16{65534, 65535, 0, 1, 2}
	want := []uint64{65534, 65535, 65536, 65537, 65538}
	for i, s := range seqs {
		if got := u.Unwrap(s); got != want[i] {
			t.Fatalf("unwrap(%d) = %d, want %d", s, got, want[i])
		}
	}
}

func TestUnwrapLateFromPreviousCycle(t *testing.T) {
	u := &Unwrapper{}
	u.Unwrap(65534)
	u.Unwrap(65535)
	u.Unwrap(0) // → 65536
	if got := u.Unwrap(65533); got != 65533 {
		t.Fatalf("late packet from previous cycle = %d, want 65533", got)
	}
}

func TestUnwrapWrapWithReorder(t *testing.T) {
	u := &Unwrapper{}
	// 65535 is held and arrives after 0 has wrapped forward.
	u.Unwrap(65534)
	if got := u.Unwrap(0); got != 65536 {
		t.Fatalf("wrap = %d, want 65536", got)
	}
	if got := u.Unwrap(65535); got != 65535 {
		t.Fatalf("held packet = %d, want 65535", got)
	}
	// The cycle counter must survive the late packet.
	if got := u.Unwrap(1); got != 65537 {
		t.Fatalf("after held packet = %d, want 65537", got)
	}
}

func TestParseAndNormalize(t *testing.T) {
	u := &Unwrapper{}
	p, err := ParseRTP(mkRTP(t, 7, 9000))
	if err != nil {
		t.Fatal(err)
	}
	sp := Normalize(p, u)
	if sp.ExtSeq != 7 || sp.Timestamp != 9000 || sp.PayloadSize != 4 || sp.SSRC != 42 {
		t.Fatalf("%+v", sp)
	}
	if _, err := ParseRTP([]byte{0xff}); err == nil {
		t.Fatal("want parse error on garbage")
	}
}

func TestReorderWithinWindow(t *testing.T) {
	jb, err := NewJitterBuffer(8)
	if err != nil {
		t.Fatal(err)
	}
	u := &Unwrapper{}
	push := func(seq uint16) {
		p, _ := ParseRTP(mkRTP(t, seq, uint32(seq)*100))
		jb.Push(Normalize(p, u))
	}
	push(1)
	push(3) // gap at 2
	push(2) // late, recoverable
	push(4)
	var order []uint64
	for {
		p, ok := jb.Next()
		if !ok {
			break
		}
		order = append(order, p.ExtSeq)
	}
	if len(order) != 4 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("order = %v", order)
	}
	st := jb.Stats()
	if st.Recovered != 1 || st.Lost != 0 || st.Duplicates != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestDuplicateIdempotent(t *testing.T) {
	jb, _ := NewJitterBuffer(8)
	u := &Unwrapper{}
	for i := 0; i < 3; i++ {
		p, _ := ParseRTP(mkRTP(t, 5, 500))
		jb.Push(Normalize(p, u))
	}
	st := jb.Stats()
	if st.Received != 1 || st.Duplicates != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestBurstLossDeclaredAsGap(t *testing.T) {
	jb, _ := NewJitterBuffer(4)
	u := &Unwrapper{}
	push := func(seq uint16) {
		p, _ := ParseRTP(mkRTP(t, seq, uint32(seq)*100))
		jb.Push(Normalize(p, u))
	}
	push(0)
	// Burst loss: 1..5 missing; 6..9 arrive, exceeding the window.
	for s := uint16(6); s <= 9; s++ {
		push(s)
	}
	var got []uint64
	for {
		p, ok := jb.Next()
		if !ok {
			break
		}
		got = append(got, p.ExtSeq)
	}
	if len(got) != 5 || got[0] != 0 || got[1] != 6 {
		t.Fatalf("got = %v", got)
	}
	st := jb.Stats()
	if st.Lost != 5 {
		t.Fatalf("lost = %d, want 5", st.Lost)
	}
	if len(st.Gaps) != 1 || st.Gaps[0].StartSeq != 1 || st.Gaps[0].EndSeq != 5 {
		t.Fatalf("gaps = %+v", st.Gaps)
	}
	if st.Gaps[0].EndTime != 600 {
		t.Fatalf("gap end time = %d", st.Gaps[0].EndTime)
	}
}

func TestFlushDeclaresTrailingHoles(t *testing.T) {
	jb, _ := NewJitterBuffer(4)
	u := &Unwrapper{}
	push := func(seq uint16) {
		p, _ := ParseRTP(mkRTP(t, seq, uint32(seq)*100))
		jb.Push(Normalize(p, u))
	}
	push(0)
	push(2)
	for {
		if _, ok := jb.Next(); !ok {
			break
		}
	}
	out := jb.Flush()
	if len(out) != 1 || out[0].ExtSeq != 2 {
		t.Fatalf("flush = %v", out)
	}
	if st := jb.Stats(); st.Lost != 1 {
		t.Fatalf("lost = %d", st.Lost)
	}
}

func TestWindowLimits(t *testing.T) {
	if _, err := NewJitterBuffer(0); err == nil {
		t.Fatal("want error for zero window")
	}
	if _, err := NewJitterBuffer(MaxWindow + 1); err == nil {
		t.Fatal("want error for oversized window")
	}
}
