package rtp

import (
	"reflect"
	"strings"
	"testing"

	prtp "github.com/pion/rtp"

	"rtpregress/internal/media"
)

// buildTrace makes an in-memory RTP trace from raw packets in the given
// arrival order. Payloads are short marker bytes so ordering is checkable.
func buildTrace(t *testing.T, seqs []uint16, arrivals []int64, windowPad byte) *Trace {
	t.Helper()
	if len(seqs) != len(arrivals) {
		t.Fatalf("seqs/arrivals length mismatch")
	}
	var b strings.Builder
	b.WriteString("track audio\ntimescale 48000\ncodec opus\npayload 111\n")
	var tr Trace
	tr.Header = Header{Track: "audio", Timescale: 48000, Codec: "opus", PayloadType: 111}
	base := uint32(960)
	for i, sn := range seqs {
		p := &prtp.Packet{}
		p.Version = 2
		p.SequenceNumber = sn
		p.Timestamp = base + uint32(i)*960
		p.SSRC = 0xdeadbeef
		p.PayloadType = 111
		p.Marker = false
		p.Payload = []byte{windowPad, byte(i)}
		raw, err := MarshalPacket(p)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.WriteString(EncodePacketLine(raw, arrivals[i]))
		tr.Packets = append(tr.Packets, Packet{
			SequenceNumber: sn, Timestamp: p.Timestamp, SSRC: p.SSRC,
			PayloadType: 111, Payload: p.Payload, ArrivalNs: arrivals[i],
		})
	}
	tr.Size = int64(b.Len())
	return &tr
}

func parseString(t *testing.T, s string) *Trace {
	t.Helper()
	tr, err := ParseTrace(strings.NewReader(s))
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	return tr
}

func TestParseTraceRoundTrip(t *testing.T) {
	built := buildTrace(t, []uint16{10, 11, 12}, []int64{0, 20_000_000, 40_000_000}, 0)
	var b strings.Builder
	b.WriteString("track audio\ntimescale 48000\ncodec opus\npayload 111\n")
	for _, p := range built.Packets {
		b.WriteString(EncodePacketLine(rawFromPacket(t, p), p.ArrivalNs))
	}
	got := parseString(t, b.String())
	if got.Header.Track != "audio" || got.Header.Timescale != 48000 || got.Header.Codec != "opus" {
		t.Fatalf("header = %+v", got.Header)
	}
	if len(got.Packets) != 3 {
		t.Fatalf("packets = %d", len(got.Packets))
	}
	for i, p := range got.Packets {
		if p.SequenceNumber != uint16(10+i) {
			t.Fatalf("packet %d seq = %d", i, p.SequenceNumber)
		}
		if p.PayloadType != 111 {
			t.Fatalf("packet %d pt = %d", i, p.PayloadType)
		}
	}
	ns, err := got.TimestampNs(960)
	if err != nil {
		t.Fatal(err)
	}
	if ns != 20_000_000 {
		t.Fatalf("timestamp ns = %d", ns)
	}
}

func rawFromPacket(t *testing.T, p Packet) []byte {
	t.Helper()
	rp := &prtp.Packet{}
	rp.Version = 2
	rp.SequenceNumber = p.SequenceNumber
	rp.Timestamp = p.Timestamp
	rp.SSRC = p.SSRC
	rp.PayloadType = p.PayloadType
	rp.Payload = p.Payload
	raw, err := MarshalPacket(rp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func TestParseTraceRejectsBrokenPackets(t *testing.T) {
	cases := map[string]string{
		"missing track":     "timescale 48000\ncodec opus\nrtp 80ff0000 0\n",
		"missing timescale": "track audio\ncodec opus\nrtp 80ff0000 0\n",
		"missing codec":     "track audio\ntimescale 48000\nrtp 80ff0000 0\n",
		"short packet":      "track audio\ntimescale 48000\ncodec opus\nrtp 80ff00 0\n",
		"bad hex":           "track audio\ntimescale 48000\ncodec opus\nrtp zzzz 0\n",
		"bad arrival":       "track audio\ntimescale 48000\ncodec opus\nrtp 80ff0000 nope\n",
		"unknown directive": "track audio\ntimescale 48000\ncodec opus\nbogus 1\nrtp 80ff0000 0\n",
		"empty trace":       "track audio\ntimescale 48000\ncodec opus\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseTrace(strings.NewReader(src))
			if err == nil {
				t.Fatalf("%s: expected parse failure", name)
			}
			if media.KindOf(err) != media.KindInput {
				t.Fatalf("%s: kind = %s", name, media.KindOf(err))
			}
		})
	}
}

func TestParseTraceReportsByteOffset(t *testing.T) {
	src := "track audio\ntimescale 48000\ncodec opus\nrtp 80ff00 0\n"
	_, err := ParseTrace(strings.NewReader(src))
	if err == nil {
		t.Fatal("expected failure")
	}
	loc, ok := media.LocOf(err)
	if !ok {
		t.Fatalf("no location: %v", err)
	}
	if !loc.HasOffset {
		t.Fatalf("byte offset not recorded: %+v", loc)
	}
	// The bad line starts after the three header lines.
	want := int64(len("track audio\ntimescale 48000\ncodec opus\n"))
	if loc.Offset != want {
		t.Fatalf("offset = %d, want %d", loc.Offset, want)
	}
	if loc.Track != "audio" {
		t.Fatalf("track = %q", loc.Track)
	}
}

func TestReorderInOrder(t *testing.T) {
	tr := buildTrace(t, []uint16{1, 2, 3, 4, 5}, []int64{0, 1, 2, 3, 4}, 0)
	got, err := Reorder(tr, ReorderOptions{Window: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packets) != 5 || len(got.Gaps) != 0 || got.Duplicates != 0 || len(got.Late) != 0 {
		t.Fatalf("unexpected result: %+v", got)
	}
	for i, p := range got.Packets {
		if p.SequenceNumber != uint16(i+1) {
			t.Fatalf("packet %d seq %d", i, p.SequenceNumber)
		}
	}
}

func TestReorderFixesOutOfOrderWithinWindow(t *testing.T) {
	// Arrival order 1,3,2,4 is repaired inside a window of 2.
	tr := buildTrace(t, []uint16{1, 3, 2, 4}, []int64{0, 1, 2, 3}, 0)
	got, err := Reorder(tr, ReorderOptions{Window: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []uint16{1, 2, 3, 4}
	for i, p := range got.Packets {
		if p.SequenceNumber != want[i] {
			t.Fatalf("packet %d = %d, want %d (committed %v)", i, p.SequenceNumber, want[i], got.Sequences)
		}
	}
	if len(got.Gaps) != 0 || len(got.Late) != 0 {
		t.Fatalf("reorder should not report gaps/late: %+v", got)
	}
}

func TestReorderDuplicateCountedOnce(t *testing.T) {
	// Packet 2 arrives twice; only one copy commits.
	tr := buildTrace(t, []uint16{1, 2, 2, 3, 4}, []int64{0, 1, 2, 3, 4}, 0)
	got, err := Reorder(tr, ReorderOptions{Window: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", got.Duplicates)
	}
	if len(got.Packets) != 4 {
		t.Fatalf("committed = %d, want 4", len(got.Packets))
	}
	for i, p := range got.Packets {
		if p.SequenceNumber != uint16(i+1) {
			t.Fatalf("committed %d = %d", i, p.SequenceNumber)
		}
	}
}

func TestReorderGapRecordedAsLoss(t *testing.T) {
	// Sequence 2 is missing and never arrives; it must be recorded as loss.
	tr := buildTrace(t, []uint16{1, 3, 4, 5, 6}, []int64{0, 1, 2, 3, 4}, 0)
	got, err := Reorder(tr, ReorderOptions{Window: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Lost() {
		t.Fatal("expected recorded loss")
	}
	if got.Gaps.Total() != 1 {
		t.Fatalf("lost = %d, want 1", got.Gaps.Total())
	}
	if got.Gaps[0].Start != 2 || got.Gaps[0].End != 2 {
		t.Fatalf("gap = %+v", got.Gaps[0])
	}
	if len(got.Packets) != 5 {
		t.Fatalf("committed = %d, want 5", len(got.Packets))
	}
}

func TestReorderLatePacketCannotRewriteCommitted(t *testing.T) {
	// 2 arrives after the window has already committed past it.
	tr := buildTrace(t, []uint16{1, 3, 4, 2, 5}, []int64{0, 1, 2, 3, 4}, 0)
	got, err := Reorder(tr, ReorderOptions{Window: 2, Stage: StageAlign})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Late) != 1 {
		t.Fatalf("late = %d, want 1", len(got.Late))
	}
	late := got.Late[0]
	if late.SequenceNumber != 2 {
		t.Fatalf("late seq = %d", late.SequenceNumber)
	}
	if late.FirstStage != StageAlign {
		t.Fatalf("first stage = %q, want align", late.FirstStage)
	}
	if !late.Duplicate && !got.Lost() {
		// The late packet filled a gap that was already recorded as loss.
		t.Fatalf("late packet should correspond to recorded loss")
	}
	// Committed output must be unchanged by the late packet.
	want := []uint16{1, 3, 4, 5}
	if len(got.Packets) != len(want) {
		t.Fatalf("committed = %v", got.Packets)
	}
	for i, p := range got.Packets {
		if p.SequenceNumber != want[i] {
			t.Fatalf("committed %d = %d, want %d", i, p.SequenceNumber, want[i])
		}
	}
}

func TestReorderZeroWindowCommitsInArrivalOrder(t *testing.T) {
	tr := buildTrace(t, []uint16{1, 3, 2, 4}, []int64{0, 1, 2, 3}, 0)
	got, err := Reorder(tr, ReorderOptions{Window: 0})
	if err != nil {
		t.Fatal(err)
	}
	// With no window out-of-order arrivals cannot be repaired.
	if len(got.Packets) != 3 {
		t.Fatalf("committed = %d, want 3 (1,3,4)", len(got.Packets))
	}
	if got.Duplicates != 0 {
		t.Fatalf("duplicates = %d, want 0", got.Duplicates)
	}
	if len(got.Late) != 1 || got.Late[0].SequenceNumber != 2 {
		t.Fatalf("late = %+v, want packet 2", got.Late)
	}
}

func TestReorderWrapAround(t *testing.T) {
	tr := buildTrace(t, []uint16{65534, 65535, 0, 1, 2}, []int64{0, 1, 2, 3, 4}, 0)
	got, err := Reorder(tr, ReorderOptions{Window: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packets) != 5 {
		t.Fatalf("committed = %d", len(got.Packets))
	}
	wantSeqs := []uint64{65534, 65535, 65536, 65537, 65538}
	if !reflect.DeepEqual(got.Sequences, wantSeqs) {
		t.Fatalf("sequences = %v, want %v", got.Sequences, wantSeqs)
	}
}

func TestReorderLargeGapCoalesced(t *testing.T) {
	tr := buildTrace(t, []uint16{10, 20, 21}, []int64{0, 1, 2}, 0)
	got, err := Reorder(tr, ReorderOptions{Window: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Gaps.Total() != 9 {
		t.Fatalf("lost = %d, want 9", got.Gaps.Total())
	}
	if len(got.Gaps) != 1 || got.Gaps[0].Start != 11 || got.Gaps[0].End != 19 {
		t.Fatalf("gaps = %+v", got.Gaps)
	}
}

func TestReorderUnknownStageRejected(t *testing.T) {
	tr := buildTrace(t, []uint16{1}, []int64{0}, 0)
	if _, err := Reorder(tr, ReorderOptions{Window: 1, Stage: "bogus"}); err == nil {
		t.Fatal("expected unknown-stage error")
	}
}
