package stream

import (
	"context"
	"encoding/binary"
	"testing"

	"mediasync/internal/fault"
)

func parseStream(t *testing.T, data []byte) ([]Packet, []XORFrame) {
	t.Helper()
	path := writeStream(t, data)
	records, err := ReadAll(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	packets, xorRecords, err := ParseRTP(records)
	if err != nil {
		t.Fatalf("ParseRTP: %v", err)
	}
	frames, err := ParseXOR(xorRecords)
	if err != nil {
		t.Fatalf("ParseXOR: %v", err)
	}
	return packets, frames
}

func TestReorderDedupAndGaps(t *testing.T) {
	// Arrival order: 10, 13, 12 (reorder), 10 again (duplicate); seq 11 is
	// the single internal gap inside the 10..13 span (both endpoints exist).
	data := append(rtpFrame(10, 1000, []byte("aaaa")),
		rtpFrame(13, 1090, []byte("dddd"))...)
	data = append(data, rtpFrame(12, 1060, []byte("cccc"))...)
	data = append(data, rtpFrame(10, 1000, []byte("aaaa"))...)

	packets, _ := parseStream(t, data)
	report, err := Analyze(packets)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Ordered) != 4 {
		t.Fatalf("want 4 slots (10..13), got %d", len(report.Ordered))
	}
	for i, want := range []uint16{10, 11, 12, 13} {
		if report.Ordered[i].Sequence != want {
			t.Fatalf("slot %d: got seq %d want %d", i, report.Ordered[i].Sequence, want)
		}
	}
	if len(report.Missing) != 1 || report.Missing[0].From != 11 ||
		report.Missing[0].To != 11 || report.Missing[0].Len() != 1 {
		t.Fatalf("gap detection wrong: %+v", report.Missing)
	}
	if len(report.Duplicates) != 1 || report.Duplicates[0].Sequence != 10 {
		t.Fatalf("duplicate detection wrong: %+v", report.Duplicates)
	}
	if report.MissingCount() != 1 {
		t.Fatalf("missing count: %d", report.MissingCount())
	}
	// Monotonic extended timestamps follow sequence order despite reordering.
	if report.ExtendedTS[10] >= report.ExtendedTS[12] || report.ExtendedTS[12] >= report.ExtendedTS[13] {
		t.Fatalf("extended timestamps not monotonic: %+v", report.ExtendedTS)
	}
}

func TestTimestampWrapUnwrapped(t *testing.T) {
	data := append(rtpFrame(5, 0xFFFFFFF0, []byte("a")),
		rtpFrame(6, 0x00000010, []byte("b"))...)
	packets, _ := parseStream(t, data)
	report, err := Analyze(packets)
	if err != nil {
		t.Fatalf("clean wrap must unwrap: %v", err)
	}
	if report.ExtendedTS[6] <= report.ExtendedTS[5] {
		t.Fatalf("wrap not extended: %+v", report.ExtendedTS)
	}
	if report.ExtendedTS[6]-report.ExtendedTS[5] != 0x20 {
		t.Fatalf("wrap delta wrong: %d", report.ExtendedTS[6]-report.ExtendedTS[5])
	}
}

func TestAmbiguousHalfWrapReported(t *testing.T) {
	data := append(rtpFrame(1, 0, []byte("a")),
		rtpFrame(2, 1<<31, []byte("b"))...)
	packets, _ := parseStream(t, data)
	_, err := Analyze(packets)
	f, ok := fault.AsFault(err)
	if !ok || f.Code != fault.ErrWrapAmbiguous || f.Offset < 0 {
		t.Fatalf("want attributed wrap ambiguity, got %v", err)
	}
}

func TestSingleLossRecoveredByXOR(t *testing.T) {
	// Three payloads, one XOR frame covering all three; packet 11 is lost.
	p0, p2 := []byte("ABCD"), []byte{1, 2, 3, 4}
	lost := []byte{9, 8, 7, 6}
	x := xorBytes(p0, lost, p2)
	data := append(rtpFrame(10, 100, p0), xorFrame(10, 3, x)...)
	data = append(data, rtpFrame(12, 140, p2)...)

	packets, frames := parseStream(t, data)
	report, err := Analyze(packets)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Recover(report, frames)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || res.Unrecovered != 0 {
		t.Fatalf("want 1 recovered 0 unrecovered, got %+v", res)
	}
	got := report.Ordered[1]
	if !got.Recovered || string(got.Payload) != string(lost) {
		t.Fatalf("recovered payload mismatch: %+v", got)
	}
	if got.Offset < 0 {
		t.Fatal("recovered packet must carry the redundancy frame offset")
	}
}

func TestDoubleLossNotRecovered(t *testing.T) {
	// Four equal-length packets covered by one XOR frame; the two middle
	// packets (11, 12) are lost. Endpoints 10 and 13 are received so the gap
	// is enumerable, but one XOR frame cannot solve two unknowns.
	p0, p3 := []byte("ABCD"), []byte{5, 6, 7, 8}
	lost1, lost2 := []byte("wxyz"), []byte{9, 9, 9, 9}
	x := xorBytes(p0, lost1, lost2, p3)
	data := append(rtpFrame(10, 100, p0), xorFrame(10, 4, x)...)
	data = append(data, rtpFrame(13, 160, p3)...)

	packets, frames := parseStream(t, data)
	report, err := Analyze(packets)
	if err != nil {
		t.Fatal(err)
	}
	if report.MissingCount() != 2 {
		t.Fatalf("precondition: want 2 missing, got %d (%+v)", report.MissingCount(), report.Missing)
	}
	res, err := Recover(report, frames)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 0 || res.Unrecovered != 2 {
		t.Fatalf("want 0 recovered 2 unrecovered, got %+v", res)
	}
}

func TestTruncatedFrameRejectedWithOffset(t *testing.T) {
	good := rtpFrame(1, 100, []byte("a"))
	// Declare a 20-byte RTP frame but supply only one payload byte.
	bad := make([]byte, FrameHeaderSize+1)
	binary.BigEndian.PutUint32(bad, 20)
	bad[4] = KindRTP
	data := append(good, bad...)
	path := writeStream(t, data)
	_, err := ReadAll(context.Background(), path)
	f, ok := fault.AsFault(err)
	if !ok || f.Code != fault.ErrTruncated || f.Offset != int64(len(good)) {
		t.Fatalf("want truncated fault at %d, got %v", len(good), err)
	}
}

func TestMalformedRTPRejected(t *testing.T) {
	data := frame(KindRTP, []byte{0x00}) // version 0, invalid
	path := writeStream(t, data)
	records, err := ReadAll(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseRTP(records); err == nil {
		t.Fatal("malformed RTP must be rejected")
	} else if f, ok := fault.AsFault(err); !ok || f.Code != fault.ErrPacketMalformed {
		t.Fatalf("want packet_malformed, got %v", err)
	}
}

func TestCancelationStopsReading(t *testing.T) {
	data := rtpFrame(1, 100, []byte("a"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReadAll(ctx, writeStream(t, data))
	f, ok := fault.AsFault(err)
	if !ok || f.Code != fault.ErrCanceled {
		t.Fatalf("want canceled fault, got %v", err)
	}
}
