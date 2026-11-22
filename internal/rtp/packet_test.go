package rtp

import (
	"encoding/binary"
	"testing"

	"github.com/pion/rtp"

	"example.com/mediaforge/internal/merrors"
)

func buildPacket(t *testing.T, ssrc uint32, seq, ts uint16, timestamp uint32, payload []byte) []byte {
	t.Helper()
	var h rtp.Header
	h.Version = 2
	h.PayloadType = 96
	h.SSRC = ssrc
	h.SequenceNumber = seq
	_ = ts
	h.Timestamp = timestamp
	raw, err := h.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, payload...)
}

func TestParseValid(t *testing.T) {
	raw := buildPacket(t, 0xdeadbeef, 7, 0, 9000, []byte("payload"))
	p, err := Parse(raw, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if p.SSRC != 0xdeadbeef || p.Seq != 7 || p.Timestamp != 9000 {
		t.Fatalf("parsed = %+v", p)
	}
	if string(p.Payload) != "payload" {
		t.Fatalf("payload = %q", p.Payload)
	}
	if p.ArrivalNanos != 1234 {
		t.Fatalf("arrival = %d", p.ArrivalNanos)
	}
	// The payload must be a copy, not an alias of the input.
	raw[len(raw)-1] = 'X'
	if string(p.Payload) == "payloaX" {
		t.Fatal("payload aliases input buffer")
	}
}

func TestParseTruncated(t *testing.T) {
	_, err := Parse([]byte{0x80, 0x60, 0x00}, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if cat, ok := merrors.CategoryOf(err); !ok || cat != merrors.Truncated {
		t.Fatalf("category = %v (%v)", cat, err)
	}
	if stage, _ := merrors.StageOf(err); stage != merrors.StageRTPParse {
		t.Fatalf("stage = %v", stage)
	}
	var me *merrors.Error
	if ok := asMediaError(err, &me); !ok || !me.HasOff {
		t.Fatalf("missing offset: %v", err)
	}
}

func TestParseOversizeAndBadVersion(t *testing.T) {
	big := make([]byte, MaxPacketSize+1)
	big[0] = 0x80
	if _, err := Parse(big, 0); !merrors.IsCategory(err, merrors.Bounds) {
		t.Fatalf("oversize err = %v", err)
	}
	raw := buildPacket(t, 1, 1, 0, 0, nil)
	raw[0] = 0x40 // version 1
	if _, err := Parse(raw, 0); !merrors.IsCategory(err, merrors.Malformed) {
		t.Fatalf("version err = %v", err)
	}
}

func asMediaError(err error, target **merrors.Error) bool {
	for err != nil {
		if me, ok := err.(*merrors.Error); ok {
			*target = me
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestSeqDeltaWrap(t *testing.T) {
	if got := SeqDelta(0, 65535); got != 1 {
		t.Fatalf("delta = %d, want 1", got)
	}
	if got := SeqDelta(65535, 0); got != -1 {
		t.Fatalf("delta = %d, want -1", got)
	}
	if got := SeqDelta(5, 5); got != 0 {
		t.Fatalf("delta = %d", got)
	}
}

func push(t *testing.T, r *Reorderer, seq uint16) ([]Packet, []Gap, error) {
	t.Helper()
	return r.Push(Packet{SSRC: 1, Seq: seq, Payload: []byte{byte(seq)}})
}

func seqs(pkts []Packet) []uint16 {
	out := make([]uint16, len(pkts))
	for i, p := range pkts {
		out[i] = p.Seq
	}
	return out
}

func TestReorderInOrder(t *testing.T) {
	r, err := NewReorderer(1, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	var all []uint16
	for seq := uint16(100); seq < 110; seq++ {
		out, _, err := push(t, r, seq)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, seqs(out)...)
	}
	if len(all) != 10 {
		t.Fatalf("emitted %d packets, want 10", len(all))
	}
	for i := uint16(0); i < 10; i++ {
		if all[i] != 100+i {
			t.Fatalf("out of order at %d: %d", i, all[i])
		}
	}
}

func TestReorderSwapped(t *testing.T) {
	r, _ := NewReorderer(1, DefaultConfig())
	if out, _, err := push(t, r, 100); err != nil || len(out) != 1 {
		t.Fatalf("first push: %v %v", out, err)
	}
	if out, _, _ := push(t, r, 102); len(out) != 0 {
		t.Fatalf("held packet emitted too early: %v", seqs(out))
	}
	out, gaps, err := push(t, r, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("unexpected gaps: %v", gaps)
	}
	if got := seqs(out); len(got) != 2 || got[0] != 101 || got[1] != 102 {
		t.Fatalf("emitted = %v", got)
	}
}

func TestReorderDuplicateNeverEmittedTwice(t *testing.T) {
	r, _ := NewReorderer(1, Config{Window: 8, MaxBuffer: 64})
	if _, _, err := push(t, r, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := push(t, r, 1); !merrors.IsCategory(err, merrors.Duplicate) {
		t.Fatalf("dup err = %v", err)
	}
	if _, _, err := push(t, r, 3); err != nil {
		t.Fatal(err)
	}
	// Buffered duplicate.
	if _, _, err := push(t, r, 3); !merrors.IsCategory(err, merrors.Duplicate) {
		t.Fatalf("buffered dup err = %v", err)
	}
	out, _, err := push(t, r, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(out); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("emitted = %v", got)
	}
	if r.Stats().Duplicate != 2 {
		t.Fatalf("dup stat = %d", r.Stats().Duplicate)
	}
}

func TestReorderLatePacketReported(t *testing.T) {
	r, _ := NewReorderer(1, Config{Window: 4, MaxBuffer: 64})
	for seq := uint16(10); seq <= 15; seq++ {
		if _, _, err := push(t, r, seq); err != nil {
			t.Fatal(err)
		}
	}
	// A never-seen sequence number that the window has already passed.
	_, _, err := push(t, r, 8)
	if !merrors.IsCategory(err, merrors.Late) {
		t.Fatalf("late err = %v", err)
	}
	// A retransmission of an emitted packet is a duplicate, not late.
	_, _, err = push(t, r, 12)
	if !merrors.IsCategory(err, merrors.Duplicate) {
		t.Fatalf("dup err = %v", err)
	}
	if r.Stats().Late != 1 || r.Stats().Duplicate != 1 {
		t.Fatalf("stats = %+v", r.Stats())
	}
}

func TestReorderGapReportedThenContinues(t *testing.T) {
	r, _ := NewReorderer(1, Config{Window: 4, MaxBuffer: 64})
	for seq := uint16(1); seq <= 3; seq++ {
		if _, _, err := push(t, r, seq); err != nil {
			t.Fatal(err)
		}
	}
	// 4 is lost; 5..9 arrive.
	var gaps []Gap
	var emitted []uint16
	for seq := uint16(5); seq <= 9; seq++ {
		out, g, err := push(t, r, seq)
		if err != nil {
			t.Fatal(err)
		}
		emitted = append(emitted, seqs(out)...)
		gaps = append(gaps, g...)
	}
	if len(gaps) != 1 || gaps[0].From != 4 || gaps[0].Count != 1 {
		t.Fatalf("gaps = %+v", gaps)
	}
	if len(emitted) != 5 {
		t.Fatalf("emitted after gap = %v", emitted)
	}
	for i, want := range []uint16{5, 6, 7, 8, 9} {
		if emitted[i] != want {
			t.Fatalf("emitted[%d] = %d, want %d", i, emitted[i], want)
		}
	}
}

func TestReorderMultiPacketGap(t *testing.T) {
	r, _ := NewReorderer(1, Config{Window: 4, MaxBuffer: 64})
	if _, _, err := push(t, r, 1); err != nil {
		t.Fatal(err)
	}
	// 2,3,4 are lost; enough later data arrives to slide the window past.
	var out []uint16
	var gaps []Gap
	for seq := uint16(5); seq <= 9; seq++ {
		o, g, err := push(t, r, seq)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, seqs(o)...)
		gaps = append(gaps, g...)
	}
	if len(gaps) != 1 || gaps[0].From != 2 || gaps[0].Count != 3 {
		t.Fatalf("gaps = %+v", gaps)
	}
	if len(out) != 5 {
		t.Fatalf("emitted = %v", out)
	}
}

func TestReorderAcrossSeqWrap(t *testing.T) {
	r, _ := NewReorderer(1, DefaultConfig())
	if _, _, err := push(t, r, 65534); err != nil {
		t.Fatal(err)
	}
	if out, _, err := push(t, r, 0); err != nil {
		t.Fatalf("wrap push: %v", err)
	} else if len(out) != 0 {
		t.Fatalf("held: %v", seqs(out))
	}
	out, gaps, err := push(t, r, 65535)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("gaps = %+v", gaps)
	}
	if got := seqs(out); len(got) != 2 || got[0] != 65535 || got[1] != 0 {
		t.Fatalf("emitted = %v", got)
	}
}

func TestReorderFlushTrailingGap(t *testing.T) {
	r, _ := NewReorderer(1, DefaultConfig())
	for seq := uint16(1); seq <= 3; seq++ {
		if _, _, err := push(t, r, seq); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := push(t, r, 6); err != nil {
		t.Fatal(err)
	}
	out, gaps := r.Flush()
	if len(out) != 1 || out[0].Seq != 6 {
		t.Fatalf("flush out = %v", seqs(out))
	}
	if len(gaps) != 2 {
		t.Fatalf("flush gaps = %+v", gaps)
	}
	if gaps[0].From != 4 || gaps[0].Count != 1 || gaps[1].From != 5 || gaps[1].Count != 1 {
		t.Fatalf("flush gaps = %+v", gaps)
	}
}

func TestReorderConfigValidation(t *testing.T) {
	if _, err := NewReorderer(1, Config{Window: 0, MaxBuffer: 8}); err == nil {
		t.Fatal("expected window error")
	}
	if _, err := NewReorderer(1, Config{Window: 1 << 15, MaxBuffer: 1 << 20}); err == nil {
		t.Fatal("expected oversized window error")
	}
	r, _ := NewReorderer(1, DefaultConfig())
	if _, _, err := r.Push(Packet{SSRC: 2, Seq: 1}); !merrors.IsCategory(err, merrors.Invalid) {
		t.Fatalf("ssrc err = %v", err)
	}
}

func TestReorderBufferOverflow(t *testing.T) {
	r, _ := NewReorderer(1, Config{Window: 32, MaxBuffer: 8})
	if _, _, err := push(t, r, 1); err != nil {
		t.Fatal(err)
	}
	// Fill the buffer without advancing: every held packet is ahead of the
	// missing sequence 2, and the window is wide enough to keep them all.
	for seq := uint16(3); seq <= 10; seq++ {
		if _, _, err := push(t, r, seq); err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
	}
	_, _, err := push(t, r, 11)
	if !merrors.IsCategory(err, merrors.Overflow) {
		t.Fatalf("overflow err = %v", err)
	}
}

func TestSortByArrivalDeterministic(t *testing.T) {
	pkts := []Packet{
		{Seq: 3, ArrivalNanos: 20},
		{Seq: 2, ArrivalNanos: 10},
		{Seq: 1, ArrivalNanos: 10},
		{Seq: 4, ArrivalNanos: 20},
	}
	SortByArrival(pkts)
	want := []uint16{1, 2, 3, 4}
	for i, p := range pkts {
		if p.Seq != want[i] {
			t.Fatalf("order = %v", seqs(pkts))
		}
	}
}

func TestParseHeaderOnly(t *testing.T) {
	// A header with no payload is valid RTP.
	raw := buildPacket(t, 99, 1, 0, 0, nil)
	p, err := Parse(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Payload) != 0 {
		t.Fatalf("payload = %q", p.Payload)
	}
	// Sanity: big-endian header fields are where we expect them.
	if binary.BigEndian.Uint32(raw[8:12]) != 99 {
		t.Fatal("fixture builder mismatch")
	}
}
