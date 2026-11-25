package rtp

import (
	"testing"

	"archive.example/mediakernel/internal/events"
)

func newTestReorderer(t *testing.T, late int64) *Reorderer {
	t.Helper()
	r, err := New(Config{Track: events.TrackRTP, LateWindow: late, Warmup: 4, MaxBuffer: 32})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func pushAll(t *testing.T, r *Reorderer, seqs []uint32) []Packet {
	t.Helper()
	var out []Packet
	for i, s := range seqs {
		res, err := r.Push(Packet{Seq: s, Timestamp: uint32(i) * 3000, ByteOffset: int64(i) * 12})
		if err != nil {
			t.Fatalf("push seq %d: %v", s, err)
		}
		out = append(out, res.Deliver...)
	}
	res := r.Flush()
	out = append(out, res.Deliver...)
	return out
}

func TestOrderedStreamDeliversEverything(t *testing.T) {
	r := newTestReorderer(t, 2)
	got := pushAll(t, r, []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	if len(got) != 10 {
		t.Fatalf("got %d packets", len(got))
	}
	for i, p := range got {
		if want := uint32(i + 1); p.Seq != want {
			t.Fatalf("position %d: got seq %d want %d", i, p.Seq, want)
		}
	}
	if s := r.Stats(); s.Lost != 0 || s.Duplicate != 0 || s.OutOfOrder != 0 {
		t.Fatalf("unexpected stats: %+v", s)
	}
	if r.Status() != StatusOrdered {
		t.Fatalf("status %s", r.Status())
	}
}

func TestDuplicateCountedOnce(t *testing.T) {
	r := newTestReorderer(t, 2)
	// 1..8 warm up and deliver; then repeat 3 and 4 twice.
	got := pushAll(t, r, []uint32{1, 2, 3, 4, 5, 6, 7, 8})
	if len(got) != 8 {
		t.Fatalf("got %d", len(got))
	}
	before := r.Stats().Received
	for i := 0; i < 2; i++ {
		res, err := r.Push(Packet{Seq: 3, ByteOffset: 1})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Duplicate || len(res.Deliver) != 0 {
			t.Fatalf("duplicate not detected: %+v", res)
		}
	}
	if r.Stats().Duplicate != 2 {
		t.Fatalf("duplicate count %d", r.Stats().Duplicate)
	}
	if r.Stats().Received != before {
		t.Fatalf("duplicates must not count as received: %d vs %d", r.Stats().Received, before)
	}
	delivered := 0
	for _, d := range got {
		if d.Seq == 3 {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("seq 3 delivered %d times", delivered)
	}
}

func TestGapBeforeWatermarkDeclaredLostOnce(t *testing.T) {
	r := newTestReorderer(t, 2)
	// Warm up with 1..4 (retain 1..4), then 6..10 skipping 5.
	var lost []events.Gap
	for _, s := range []uint32{1, 2, 3, 4, 6, 7, 8, 9, 10} {
		res, err := r.Push(Packet{Seq: s})
		if err != nil {
			t.Fatal(err)
		}
		lost = append(lost, res.Lost...)
	}
	// Re-pushing 9 must not re-declare anything.
	if res, _ := r.Push(Packet{Seq: 9}); len(res.Lost) != 0 {
		t.Fatalf("unexpected loss on duplicate: %+v", res)
	}
	if len(lost) != 1 || lost[0].Seq != 5 {
		t.Fatalf("expected single loss for seq 5, got %+v", lost)
	}
	if lost[0].Reason != ReasonWatermark {
		t.Fatalf("reason %q", lost[0].Reason)
	}
	if lost[0].At != -1 {
		t.Fatalf("gap must not carry an invented timestamp")
	}
}

func TestLatePacketForDeclaredGapIsQueued(t *testing.T) {
	r := newTestReorderer(t, 2)
	var latePackets []Packet
	for _, s := range []uint32{1, 2, 3, 4, 6, 7, 8, 9, 10} {
		res, err := r.Push(Packet{Seq: s})
		if err != nil {
			t.Fatal(err)
		}
		latePackets = append(latePackets, res.Late...)
	}
	if len(latePackets) != 0 {
		t.Fatalf("no late packets expected yet")
	}
	// 5 arrives after its hole crossed the watermark.
	res, err := r.Push(Packet{Seq: 5, ByteOffset: 55})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Late) != 1 || res.Late[0].Seq != 5 {
		t.Fatalf("late packet not queued: %+v", res)
	}
	if len(res.Deliver) != 0 {
		t.Fatalf("late packet must not bypass ordered delivery")
	}
	if r.Stats().Late != 1 {
		t.Fatalf("late count %d", r.Stats().Late)
	}
}

func TestLatePacketReplaysToSameDigestAsOrderedInput(t *testing.T) {
	// Ordered reference: every packet arrives in order.
	ordered := newTestReorderer(t, 2)
	var orderedEvents []events.Event
	for _, s := range []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		res, err := ordered.Push(Packet{Seq: s, Payload: []byte{byte(s)}})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range res.Deliver {
			orderedEvents = append(orderedEvents, eventOf(p))
		}
	}
	orderedEvents = append(orderedEvents, deliveredEvents(ordered.Flush())...)

	// Damaged: 5 lost behind the watermark, then supplied late.
	damaged := newTestReorderer(t, 2)
	var delivered []events.Event
	var late []Packet
	for _, s := range []uint32{1, 2, 3, 4, 6, 7, 8, 9, 10} {
		res, err := damaged.Push(Packet{Seq: s, Payload: []byte{byte(s)}})
		if err != nil {
			t.Fatal(err)
		}
		delivered = append(delivered, deliveredEvents(res)...)
		late = append(late, res.Late...)
	}
	res, err := damaged.Push(Packet{Seq: 5, Payload: []byte{5}})
	if err != nil {
		t.Fatal(err)
	}
	late = append(late, res.Late...)
	delivered = append(delivered, deliveredEvents(damaged.Flush())...)
	// The recovered packet is part of the replay set, not the ordered stream.
	if len(late) != 1 {
		t.Fatalf("late set %v", late)
	}
	for _, p := range late {
		delivered = append(delivered, eventOf(p))
	}

	a := events.StateBytes(0, orderedEvents, nil)
	b := events.StateBytes(0, delivered, nil)
	if string(a) != string(b) {
		t.Fatalf("replayed state differs from ordered state\n--- ordered ---\n%s\n--- damaged ---\n%s", a, b)
	}
}

func eventOf(p Packet) events.Event {
	return events.Event{
		Track:   events.TrackRTP,
		Kind:    events.KindRTP,
		Seq:     uint64(p.Seq),
		At:      int64(p.Timestamp),
		Payload: append([]byte(nil), p.Payload...),
	}
}

func deliveredEvents(res PushResult) []events.Event {
	out := make([]events.Event, 0, len(res.Deliver))
	for _, p := range res.Deliver {
		out = append(out, eventOf(p))
	}
	return out
}

func TestSequenceWrap(t *testing.T) {
	r := newTestReorderer(t, 2)
	seqs := []uint32{65532, 65533, 65534, 65535, 0, 1, 2, 3, 4, 5}
	got := pushAll(t, r, seqs)
	if len(got) != len(seqs) {
		t.Fatalf("got %d want %d", len(got), len(seqs))
	}
	// Raw sequence values remain the identity across the wrap.
	for i, p := range got {
		if p.Seq != seqs[i] {
			t.Fatalf("position %d got %d want %d", i, p.Seq, seqs[i])
		}
	}
	if r.Stats().OutOfOrder != 0 {
		t.Fatalf("wrap must not look reordered: %+v", r.Stats())
	}
}

func TestReorderedOpeningDoesNotStrandEarlierPackets(t *testing.T) {
	r := newTestReorderer(t, 2)
	// The opening arrives reordered: 12, 10, 11, 9, 13, 14, 15, 16.
	got := pushAll(t, r, []uint32{12, 10, 11, 9, 13, 14, 15, 16})
	if len(got) != 8 {
		t.Fatalf("got %d packets: %v", len(got), got)
	}
	if got[0].Seq != 9 || got[1].Seq != 10 || got[2].Seq != 11 || got[3].Seq != 12 {
		t.Fatalf("opening not resequenced: %d %d %d %d", got[0].Seq, got[1].Seq, got[2].Seq, got[3].Seq)
	}
	for i := 1; i < len(got); i++ {
		if got[i].ExtSeq <= got[i-1].ExtSeq {
			t.Fatalf("delivery not strictly ascending at %d", i)
		}
	}
}

func TestBurstLossReportsAllHoles(t *testing.T) {
	r := newTestReorderer(t, 3)
	var lost []events.Gap
	for _, s := range []uint32{1, 2, 3, 4, 5, 10, 11, 12, 13, 14} {
		res, err := r.Push(Packet{Seq: s})
		if err != nil {
			t.Fatal(err)
		}
		lost = append(lost, res.Lost...)
	}
	lost = append(lost, r.Flush().Lost...)
	if len(lost) < 4 {
		t.Fatalf("expected burst holes to be reported, got %+v", lost)
	}
	seen := map[uint64]bool{}
	for _, g := range lost {
		if seen[g.Seq] {
			t.Fatalf("gap %d declared twice", g.Seq)
		}
		seen[g.Seq] = true
	}
}

func TestPushRawMalformedPacketReportsOffset(t *testing.T) {
	r := newTestReorderer(t, 2)
	_, err := r.PushRaw([]byte{0x80}, 4096)
	if err == nil {
		t.Fatal("expected malformed error")
	}
	if err.Code != events.CodeMalformed || err.Offset != 4096 || err.Track != events.TrackRTP {
		t.Fatalf("error lacks context: %v", err)
	}
}

func TestPushRawParsesPionPacket(t *testing.T) {
	r := newTestReorderer(t, 2)
	// Minimal RTP header: version 2, payload type 96, seq 7, timestamp 9000.
	raw := []byte{0x80, 96, 0x00, 0x07, 0x00, 0x00, 0x23, 0x28, 0, 0, 0, 1, 0xAA, 0xBB}
	res, err := r.PushRaw(raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deliver == nil && len(res.Deliver) != 0 {
		t.Fatal("unexpected")
	}
	if r.Stats().Received != 1 {
		t.Fatalf("received %d", r.Stats().Received)
	}
}

func TestMaxSpanRejection(t *testing.T) {
	r, err := New(Config{Track: events.TrackRTP, LateWindow: 2, MaxSpan: 100, Warmup: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, e := r.Push(Packet{Seq: 0}); e != nil {
		t.Fatal(e)
	}
	_, e := r.Push(Packet{Seq: 40000, ByteOffset: 7})
	if e == nil || e.Code != events.CodeCapacity {
		t.Fatalf("expected span rejection, got %v", e)
	}
	if e.Offset != 7 {
		t.Fatalf("offset lost: %v", e)
	}
}

func TestNewRejectsImpossibleConfig(t *testing.T) {
	if _, err := New(Config{LateWindow: -1}); err == nil {
		t.Fatal("negative window accepted")
	}
	if _, err := New(Config{LateWindow: 70000}); err == nil {
		t.Fatal("window larger than span accepted")
	}
}

func TestDeterministicAcrossTwoRuns(t *testing.T) {
	run := func() string {
		r := newTestReorderer(t, 3)
		var evs []events.Event
		for _, s := range []uint32{5, 4, 6, 6, 7, 12, 8, 9, 13, 10, 11, 14, 15} {
			res, err := r.Push(Packet{Seq: s, Payload: []byte{byte(s)}})
			if err != nil {
				t.Fatal(err)
			}
			evs = append(evs, deliveredEvents(res)...)
		}
		evs = append(evs, deliveredEvents(r.Flush())...)
		return string(events.StateBytes(r.Stats().Watermark, evs, nil))
	}
	if a, b := run(), run(); a != b {
		t.Fatalf("nondeterministic reorder\n%s\n%s", a, b)
	}
}
