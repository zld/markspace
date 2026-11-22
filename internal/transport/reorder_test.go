package transport

import (
	"encoding/binary"
	"testing"

	"mediadist/internal/timeline"
)

// mkPacket builds a minimal valid RTP packet with the given sequence number,
// timestamp and payload length.
func mkPacket(stream int, seq uint16, ts uint32, payload int) *Packet {
	raw := make([]byte, 12+payload)
	raw[0] = 0x80
	raw[1] = 96
	binary.BigEndian.PutUint16(raw[2:4], seq)
	binary.BigEndian.PutUint32(raw[4:8], ts)
	binary.BigEndian.PutUint32(raw[8:12], 0xDEADBEEF)
	for i := 12; i < len(raw); i++ {
		raw[i] = byte(i)
	}
	p, err := Parse(raw, ParseOptions{Stream: stream})
	if err != nil {
		panic(err)
	}
	return p
}

func TestParseRejectsShortAndKeepsSequence(t *testing.T) {
	if _, err := Parse([]byte{0x80, 0x60}, ParseOptions{Stream: 1}); err == nil {
		t.Fatal("expected short header rejection")
	}
	if _, err := Parse(make([]byte, 11), ParseOptions{Stream: 1}); err == nil {
		t.Fatal("expected 11-byte rejection")
	}
	// A 12-byte header with a CSRC count of 15 claims 72 bytes: reject but
	// still report the sequence number.
	bad := make([]byte, 12)
	bad[0] = 0x8F
	binary.BigEndian.PutUint16(bad[2:4], 4242)
	p, err := Parse(bad, ParseOptions{Stream: 2})
	if err == nil {
		t.Fatal("expected CSRC overflow rejection")
	}
	if p.Seq != 4242 {
		t.Fatalf("sequence %d want 4242", p.Seq)
	}

	// Padding that claims more bytes than present is recoverable.
	padded := make([]byte, 16)
	padded[0] = 0xA0 // P bit
	padded[1] = 96
	binary.BigEndian.PutUint16(padded[2:4], 7)
	padded[15] = 200
	_, err = Parse(padded, ParseOptions{Stream: 0, Strict: true})
	if err == nil {
		t.Fatal("expected padding rejection")
	}
}

func TestParseStreamSkipsBadDatagrams(t *testing.T) {
	var dgs [][]byte
	for i := 0; i < 5; i++ {
		dgs = append(dgs, mkPacket(0, uint16(100+i), 0, 8).Raw)
	}
	dgs = append(dgs[:2], append([][]byte{{0x00}}, dgs[2:]...)...)
	res, err := ParseStream(0, dgs, DefaultLimits(), false)
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if len(res.Packets) != 5 {
		t.Fatalf("accepted %d want 5", len(res.Packets))
	}
	if res.Skipped != 1 || len(res.Errors) != 1 {
		t.Fatalf("skipped %d errors %d", res.Skipped, len(res.Errors))
	}
	if res.Errors[0].Stage != "transport" {
		t.Fatalf("stage %s", res.Errors[0].Stage)
	}
}

func TestReorderDedupeAndOrder(t *testing.T) {
	buf, err := NewReorderBuffer(ReorderConfig{Stream: 0, Window: 4}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	// Arrival order: 1, 3, 2, 2(dup), 4, 5. Window 4 holds back only the
	// newest 4 sequence numbers, so all of these commit by flush time.
	var emitted []uint16
	push := func(seq uint16) {
		for _, p := range buf.Push(mkPacket(0, seq, uint32(seq)*100, 4)) {
			emitted = append(emitted, p.Seq)
		}
	}
	push(1)
	push(3)
	push(2)
	push(2)
	push(4)
	push(5)
	for _, p := range buf.Flush() {
		emitted = append(emitted, p.Seq)
	}
	want := []uint16{1, 2, 3, 4, 5}
	if len(emitted) != len(want) {
		t.Fatalf("emitted %v want %v", emitted, want)
	}
	for i := range want {
		if emitted[i] != want[i] {
			t.Fatalf("emitted %v want %v", emitted, want)
		}
	}
	st := buf.Stats()
	if st.Duplicate != 1 {
		t.Fatalf("duplicates %d", st.Duplicate)
	}
	if st.Reordered != 1 {
		t.Fatalf("reordered %d", st.Reordered)
	}
	if len(buf.Gaps()) != 0 {
		t.Fatalf("unexpected gaps %v", buf.Gaps())
	}
}

func TestReorderLatePacketDoesNotRewriteCommittedFragment(t *testing.T) {
	buf, _ := NewReorderBuffer(ReorderConfig{Stream: 3, Window: 2}, DefaultLimits())
	var first []uint16
	for _, seq := range []uint16{10, 11, 12, 13, 14, 15} {
		for _, p := range buf.Push(mkPacket(3, seq, uint32(seq), 4)) {
			first = append(first, p.Seq)
		}
	}
	if len(first) == 0 {
		t.Fatal("expected some committed packets before flush")
	}
	// Sequence 10 is already committed. A late copy must not be emitted.
	late := buf.Push(mkPacket(3, 10, 10, 4))
	if len(late) != 0 {
		t.Fatalf("late packet was emitted: %v", late)
	}
	st := buf.Stats()
	if st.Late != 1 {
		t.Fatalf("late %d", st.Late)
	}
	if errs := buf.Errors(); len(errs) == 0 || errs[len(errs)-1].Code != "late_packet" {
		t.Fatalf("late packet not recorded: %v", errs)
	}
}

func TestReorderGapCommitAndRepairBeforeCommit(t *testing.T) {
	buf, _ := NewReorderBuffer(ReorderConfig{Stream: 1, Window: 3}, DefaultLimits())
	rec := NewLossRecorder(1, 16)
	// 1..6 commit; 7 and 8 are missing; 9 bounds the uncommitted tail. A
	// three-packet window keeps the tail repairable until flush.
	var order []uint16
	recordGap := func() {
		if lo, hi := buf.CommittedGap(); lo >= 0 && hi >= lo {
			rec.RecordLost(lo, hi)
		}
	}
	for _, seq := range []uint16{1, 2, 3, 4, 5, 6, 9} {
		for _, p := range buf.Push(mkPacket(1, seq, uint32(seq), 4)) {
			order = append(order, p.Seq)
		}
		recordGap()
	}
	if len(rec.Intervals()) != 0 {
		t.Fatalf("no committed gaps yet, got %+v", rec.Intervals())
	}
	pending := buf.PendingGaps()
	if len(pending) != 1 || pending[0].First != 7 || pending[0].Last != 8 {
		t.Fatalf("pending gaps %+v", pending)
	}
	rec.RecordPending(pending[0].First, pending[0].Last)
	for _, seq := range []uint16{7, 8} {
		if !rec.Recover(int64(seq)) {
			t.Fatalf("repair of %d should be accepted before commit", seq)
		}
		for _, p := range buf.PushRecovered(mkPacket(1, seq, uint32(seq), 4)) {
			order = append(order, p.Seq)
		}
		recordGap()
	}
	for _, p := range buf.Flush() {
		order = append(order, p.Seq)
	}
	recordGap()
	lost, recovered := rec.Counts()
	if lost != 0 || recovered != 2 {
		t.Fatalf("lost=%d recovered=%d", lost, recovered)
	}
	if rec.RecoveredBursts() != 1 {
		t.Fatalf("recovered bursts %d", rec.RecoveredBursts())
	}
	if len(order) != 9 {
		t.Fatalf("emitted %v", order)
	}
	for i, seq := range order {
		if seq != uint16(i+1) {
			t.Fatalf("order %v", order)
		}
	}
}

func TestReorderSequenceWrap(t *testing.T) {
	buf, _ := NewReorderBuffer(ReorderConfig{Stream: 2, Window: 2}, DefaultLimits())
	var emitted []uint16
	seqs := []uint16{65533, 65534, 65535, 0, 1, 2}
	for _, seq := range seqs {
		for _, p := range buf.Push(mkPacket(2, seq, uint32(seq), 4)) {
			emitted = append(emitted, p.Seq)
		}
	}
	for _, p := range buf.Flush() {
		emitted = append(emitted, p.Seq)
	}
	if len(emitted) != len(seqs) {
		t.Fatalf("emitted %v", emitted)
	}
	for i := range seqs {
		if emitted[i] != seqs[i] {
			t.Fatalf("wrap order %v want %v", emitted, seqs)
		}
	}
	if len(buf.Gaps()) != 0 {
		t.Fatalf("wrap produced gaps %v", buf.Gaps())
	}
}

func TestLossRecorderRecoverBeforeCommit(t *testing.T) {
	rec := NewLossRecorder(0, 8)
	rec.RecordPending(10, 14)
	// A retransmission arriving while the hole is still pending is accepted;
	// the sequence never enters lost evidence.
	if !rec.Recover(12) {
		t.Fatal("repair of a pending sequence must succeed")
	}
	rec.RecordLost(10, 14)
	lost, recovered := rec.Counts()
	if lost != 4 || recovered != 1 {
		t.Fatalf("lost=%d recovered=%d", lost, recovered)
	}
	iv := rec.Intervals()
	// Lost intervals come first (ascending), then recovered runs: [10,11],
	// [13,14], then [12,12].
	if len(iv) != 3 {
		t.Fatalf("intervals %+v", iv)
	}
	if iv[0].First != 10 || iv[0].Last != 11 || iv[0].State != LossLost {
		t.Fatalf("first interval %+v", iv[0])
	}
	if iv[1].First != 13 || iv[1].Last != 14 || iv[1].State != LossLost {
		t.Fatalf("second interval %+v", iv[1])
	}
	if iv[2].First != 12 || iv[2].State != LossRecovered {
		t.Fatalf("recovered interval %+v", iv[2])
	}
	if rec.LostBursts() != 2 || rec.RecoveredBursts() != 1 {
		t.Fatalf("bursts lost=%d recovered=%d", rec.LostBursts(), rec.RecoveredBursts())
	}
	// A repair attempt for a committed sequence is refused and recorded as a
	// late arrival, never rewriting the committed fragment.
	if rec.Recover(13) {
		t.Fatal("repair after commit must be refused")
	}
	if len(rec.Late()) != 1 {
		t.Fatalf("late %v", rec.Late())
	}
}

func TestLossRecorderMergesTouchingRuns(t *testing.T) {
	rec := NewLossRecorder(0, 8)
	rec.RecordLost(10, 12)
	rec.RecordLost(13, 15)
	iv := rec.Intervals()
	if len(iv) != 1 || iv[0].First != 10 || iv[0].Last != 15 {
		t.Fatalf("intervals %+v", iv)
	}
	// A recovery before a later re-report is excluded from the new run.
	rec.RecordPending(20, 20)
	if !rec.Recover(20) {
		t.Fatal("recover of a pending sequence is accepted")
	}
	rec.RecordLost(9, 21)
	lost, recovered := rec.Counts()
	if lost != 12 || recovered != 1 {
		t.Fatalf("lost=%d recovered=%d", lost, recovered)
	}
}

func TestEstimatorUsesWindowAndNeverTouchesTimestamps(t *testing.T) {
	clock, err := timeline.NewClock(90000, 32)
	if err != nil {
		t.Fatal(err)
	}
	est, err := NewEstimator(clock, EstimatorConfig{Window: 1e9, MinSamples: 2, MaxRateBps: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	// 90000 ticks == 1 second. Two samples of 1000 bytes one second apart:
	// 2000 bytes/s == 16000 bits/s.
	est.Add(0, 1000)
	est.Add(90000, 1000)
	rate := est.RateBps(90000)
	if rate != 16000 {
		t.Fatalf("rate %d want 16000", rate)
	}
	cap := est.BufferBytes(90000, 2e9)
	if cap != 4000 {
		t.Fatalf("buffer cap %d want 4000", cap)
	}
	reports := AggregateFragments([]FragmentSample{
		{Segment: 1, Bytes: 500, Tick: 45000},
		{Segment: 0, Bytes: 500, Tick: 0},
	}, est, 1e9)
	if len(reports) != 2 || reports[0].Index != 0 || reports[1].Index != 1 {
		t.Fatalf("reports %+v", reports)
	}
}
