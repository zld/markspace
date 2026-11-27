package rtp

import (
	"testing"

	pionrtp "github.com/pion/rtp"

	"continuity/internal/fixture"
)

func feedAll(r *Recoverer, raw [][]byte) []Committed {
	var out []Committed
	for _, b := range raw {
		var pkt pionrtp.Packet
		if err := pkt.Unmarshal(b); err != nil {
			panic(err)
		}
		out = append(out, r.Feed(&pkt)...)
	}
	return append(out, r.Flush()...)
}

func TestRecoverFixture(t *testing.T) {
	in := fixture.Generate(17, "")
	r := NewRecoverer(DefaultMaxGap)
	committed := feedAll(r, in.RTP)
	st := r.Stats
	if st.Received != 24 {
		t.Fatalf("received = %d, want 24", st.Received)
	}
	if st.Lost != 3 {
		t.Fatalf("lost = %d, want 3", st.Lost)
	}
	if st.Duplicated != 2 {
		t.Fatalf("duplicated = %d, want 2", st.Duplicated)
	}
	if st.Late != 1 {
		t.Fatalf("late = %d, want 1", st.Late)
	}
	if len(committed) != 21 {
		t.Fatalf("committed = %d, want 21", len(committed))
	}
	// Missing range covers the loss burst (ext seq 65540..65542).
	if len(st.Missing) != 1 || st.Missing[0].From != 65540 || st.Missing[0].To != 65542 {
		t.Fatalf("missing = %+v", st.Missing)
	}
	// Recovery happened at the first packet after the burst.
	if len(st.Recoveries) != 1 || st.Recoveries[0].Seq != 65543 {
		t.Fatalf("recoveries = %+v", st.Recoveries)
	}
	// Committed sequence is strictly increasing, wrap-aware, and skips
	// exactly the lost range 65540..65542.
	var want []uint64
	for ext := uint64(65530); ext <= 65553; ext++ {
		if ext >= 65540 && ext <= 65542 {
			continue
		}
		want = append(want, ext)
	}
	if len(committed) != len(want) {
		t.Fatalf("committed = %d, want %d", len(committed), len(want))
	}
	for i, c := range committed {
		if c.Ext != want[i] {
			t.Fatalf("commit %d: ext %d, want %d", i, c.Ext, want[i])
		}
	}
}

func TestSequenceWrap(t *testing.T) {
	r := NewRecoverer(DefaultMaxGap)
	mk := func(seq uint16) *pionrtp.Packet {
		return &pionrtp.Packet{Header: pionrtp.Header{Version: 2, SequenceNumber: seq}}
	}
	var committed []Committed
	for _, seq := range []uint16{65534, 65535, 0, 1, 2} {
		committed = append(committed, r.Feed(mk(seq))...)
	}
	committed = append(committed, r.Flush()...)
	if len(committed) != 5 {
		t.Fatalf("committed = %d, want 5", len(committed))
	}
	for i, want := range []uint64{65534, 65535, 65536, 65537, 65538} {
		if committed[i].Ext != want {
			t.Fatalf("commit %d ext = %d, want %d", i, committed[i].Ext, want)
		}
	}
	if r.Stats.Lost != 0 {
		t.Fatalf("lost = %d, want 0", r.Stats.Lost)
	}
}

func TestLatePacketDoesNotOverwrite(t *testing.T) {
	r := NewRecoverer(1)
	mk := func(seq uint16) *pionrtp.Packet {
		return &pionrtp.Packet{Header: pionrtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 100}}
	}
	var committed []Committed
	committed = append(committed, r.Feed(mk(10))...)
	committed = append(committed, r.Feed(mk(13))...) // gap of 2 > maxGap: 11,12 declared lost, recover at 13
	committed = append(committed, r.Flush()...)
	if len(committed) != 2 || r.Stats.Lost != 2 {
		t.Fatalf("committed=%d lost=%d", len(committed), r.Stats.Lost)
	}
	// The lost packet 11 arrives after its position was committed.
	if out := r.Feed(mk(11)); len(out) != 0 {
		t.Fatalf("late packet committed %d events", len(out))
	}
	if r.Stats.Late != 1 {
		t.Fatalf("late = %d, want 1", r.Stats.Late)
	}
	// Committed timeline is unchanged.
	if committed[1].Packet.Timestamp != 1300 {
		t.Fatalf("committed timestamp = %d, want 1300", committed[1].Packet.Timestamp)
	}
}

func TestDuplicateAndReorder(t *testing.T) {
	r := NewRecoverer(DefaultMaxGap)
	mk := func(seq uint16) *pionrtp.Packet {
		return &pionrtp.Packet{Header: pionrtp.Header{Version: 2, SequenceNumber: seq}}
	}
	var committed []Committed
	for _, seq := range []uint16{1, 3, 2, 2, 4} { // reorder + duplicate
		committed = append(committed, r.Feed(mk(seq))...)
	}
	committed = append(committed, r.Flush()...)
	if len(committed) != 4 {
		t.Fatalf("committed = %d, want 4", len(committed))
	}
	if r.Stats.Duplicated != 1 {
		t.Fatalf("duplicated = %d, want 1", r.Stats.Duplicated)
	}
	if r.Stats.Lost != 0 {
		t.Fatalf("lost = %d, want 0", r.Stats.Lost)
	}
}
