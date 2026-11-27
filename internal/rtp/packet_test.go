package rtp

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"

	pionrtp "github.com/pion/rtp"

	"media.rebuild/internal/model"
)

// capture builds a framed capture from raw datagrams.
func capture(datagrams ...[]byte) []byte {
	var b bytes.Buffer
	b.WriteString(Magic)
	for _, d := range datagrams {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(d)))
		b.Write(l[:])
		b.Write(d)
	}
	return b.Bytes()
}

func packet(ssrc uint32, seq uint16, ts uint32, payload string) []byte {
	p := &pionrtp.Packet{
		Header: pionrtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
		},
		Payload: []byte(payload),
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

func TestReadCleanStream(t *testing.T) {
	raw := capture(
		packet(7, 100, 9000, "a"),
		packet(7, 101, 12000, "bb"),
		packet(7, 102, 15000, "ccc"),
	)
	c, err := Read(bytes.NewReader(raw), "clean.rtpj", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Errors) != 0 {
		t.Fatalf("errors %v", c.Errors)
	}
	if len(c.Records) != 3 || len(c.Streams) != 1 {
		t.Fatalf("records %d streams %d", len(c.Records), len(c.Streams))
	}
	if !c.Records[0].FirstOfSSRC || c.Records[1].FirstOfSSRC {
		t.Fatal("first-of-ssrc wrong")
	}
	if c.Records[1].TimeNs != 133_333_333 {
		t.Fatalf("time ns %d", c.Records[1].TimeNs)
	}
	if c.Records[0].Offset != 4 || c.Records[1].Offset != int64(8+len(packet(7, 100, 9000, "a"))) {
		t.Fatalf("offsets %d %d", c.Records[0].Offset, c.Records[1].Offset)
	}
	dup, gaps, missing := c.Totals()
	if dup != 0 || gaps != 0 || missing != 0 {
		t.Fatalf("totals %d %d %d", dup, gaps, missing)
	}
}

func TestDuplicateIsIdempotent(t *testing.T) {
	p := packet(1, 500, 100, "x")
	raw := capture(p, p, p)
	c, _ := Read(bytes.NewReader(raw), "dup.rtpj", Options{})
	if len(c.Records) != 3 {
		t.Fatalf("records %d", len(c.Records))
	}
	if !c.Records[1].Duplicate || !c.Records[2].Duplicate {
		t.Fatal("duplicates not flagged")
	}
	if c.Streams[0].Unique != 1 || c.Streams[0].Duplicates != 2 {
		t.Fatalf("stats %+v", c.Streams[0])
	}
	if dup, _, _ := c.Totals(); dup != 2 {
		t.Fatalf("duplicate total %d", dup)
	}
	// Re-reading produces identical results: duplicates never mutate state
	// beyond counters.
	c2, _ := Read(bytes.NewReader(raw), "dup.rtpj", Options{})
	if !reflect.DeepEqual(c2.Streams, c.Streams) || !reflect.DeepEqual(c2.Records, c.Records) {
		t.Fatalf("unstable: %+v vs %+v", c2.Streams, c.Streams)
	}
}

func TestReorderAndGap(t *testing.T) {
	raw := capture(
		packet(9, 10, 0, "a"),
		packet(9, 11, 3000, "b"),
		packet(9, 13, 9000, "d"),  // 12 missing
		packet(9, 12, 6000, "c"),  // late arrival
		packet(9, 16, 18000, "g"), // 14,15 missing
	)
	c, _ := Read(bytes.NewReader(raw), "gap.rtpj", Options{})
	if len(c.Errors) != 0 {
		t.Fatalf("errors %v", c.Errors)
	}
	if !c.Records[3].Reordered {
		t.Fatal("late packet not flagged reordered")
	}
	s := c.Streams[0]
	if s.Reordered != 1 {
		t.Fatalf("reordered %d", s.Reordered)
	}
	if s.Missing != 2 || s.Gaps != 1 {
		t.Fatalf("missing %d gaps %d (%v)", s.Missing, s.Gaps, s.GapRanges)
	}
	if s.GapRanges[0] != (Range{14, 15}) {
		t.Fatalf("gaps %v", s.GapRanges)
	}
	// The late packet filled the 12-13 hole, so only 14-15 remains absent.
	if dup, gaps, missing := c.Totals(); dup != 0 || gaps != 1 || missing != 2 {
		t.Fatalf("totals %d %d %d", dup, gaps, missing)
	}
}

func TestSequenceWrap(t *testing.T) {
	raw := capture(
		packet(3, 65534, 0, "a"),
		packet(3, 65535, 3000, "b"),
		packet(3, 0, 6000, "c"),
		packet(3, 1, 9000, "d"),
	)
	c, _ := Read(bytes.NewReader(raw), "wrap.rtpj", Options{})
	if len(c.Errors) != 0 {
		t.Fatalf("wrap treated as error: %v", c.Errors)
	}
	if c.Records[2].ExtSequence != 65536 || c.Records[3].ExtSequence != 65537 {
		t.Fatalf("ext seq %d %d", c.Records[2].ExtSequence, c.Records[3].ExtSequence)
	}
	if s := c.Streams[0]; s.Gaps != 0 || s.Reordered != 0 || s.Duplicates != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestMultipleSSRCsAreIndependent(t *testing.T) {
	raw := capture(
		packet(1, 10, 0, "a"),
		packet(2, 10, 0, "b"),
		packet(1, 12, 6000, "c"),
		packet(2, 11, 3000, "d"),
	)
	c, _ := Read(bytes.NewReader(raw), "multi.rtpj", Options{})
	if len(c.Streams) != 2 {
		t.Fatalf("streams %d", len(c.Streams))
	}
	if c.Streams[0].SSRC != 1 || c.Streams[0].Missing != 1 || c.Streams[0].Gaps != 1 {
		t.Fatalf("ssrc1 %+v", c.Streams[0])
	}
	if c.Streams[1].SSRC != 2 || c.Streams[1].Missing != 0 || c.Streams[1].Gaps != 0 {
		t.Fatalf("ssrc2 %+v", c.Streams[1])
	}
}

func TestTruncatedDatagramHasStableError(t *testing.T) {
	full := capture(packet(1, 1, 0, "hello"), packet(1, 2, 3000, "world"))
	// Cut the last datagram in half.
	cut := full[:len(full)-5]
	c, err := Read(bytes.NewReader(cut), "cut.rtpj", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Records) != 1 {
		t.Fatalf("partial record fabricated: %d", len(c.Records))
	}
	if len(c.Errors) != 1 {
		t.Fatalf("errors %v", c.Errors)
	}
	e := c.Errors[0]
	if e.Code != model.CodeRTPTruncated || e.Source.Offset < 0 {
		t.Fatalf("error %+v", e)
	}
	if e.Source.Name != "cut.rtpj" {
		t.Fatalf("source name %q", e.Source.Name)
	}
	// A capture claiming a datagram but containing none truncates too.
	raw := append([]byte(Magic), 0, 0, 0, 16)
	c, _ = Read(bytes.NewReader(raw), "empty.rtpj", Options{})
	if len(c.Errors) != 1 || c.Errors[0].Code != model.CodeRTPTruncated {
		t.Fatalf("empty frame: %v", c.Errors)
	}
}

func TestShortDatagramIsErrorNotPanic(t *testing.T) {
	raw := capture([]byte{0x80, 0x60, 0x00})
	c, _ := Read(bytes.NewReader(raw), "short.rtpj", Options{})
	if len(c.Records) != 0 || len(c.Errors) != 1 || c.Errors[0].Code != model.CodeRTPTruncated {
		t.Fatalf("records %d errors %v", len(c.Records), c.Errors)
	}
}

func TestBadMagic(t *testing.T) {
	c, _ := Read(bytes.NewReader([]byte("NOPE....")), "bad.rtpj", Options{})
	if len(c.Errors) != 1 || c.Errors[0].Code != model.CodeRTPHeader {
		t.Fatalf("errors %v", c.Errors)
	}
	if len(c.Records) != 0 {
		t.Fatal("records on bad magic")
	}
}

func TestOversizeLengthRejected(t *testing.T) {
	raw := append([]byte(Magic), 0xff, 0xff, 0xff, 0xff)
	c, _ := Read(bytes.NewReader(raw), "huge.rtpj", Options{MaxDatagram: 100})
	if len(c.Errors) != 1 || c.Errors[0].Code != model.CodeRTPHeader {
		t.Fatalf("errors %v", c.Errors)
	}
}

func TestSeqSetMissing(t *testing.T) {
	var s seqSet
	for _, n := range []uint64{1, 2, 5, 6, 9} {
		if s.add(n) {
			t.Fatalf("%d reported duplicate", n)
		}
	}
	if s.add(5) != true {
		t.Fatal("repeat not duplicate")
	}
	// 1,2,5,6,9 received out of 1..9: 3,4,7,8 are absent.
	missing, gaps := s.missing(1, 9)
	if missing != 4 || len(gaps) != 2 {
		t.Fatalf("missing %d gaps %v", missing, gaps)
	}
	if gaps[0] != (Range{3, 4}) || gaps[1] != (Range{7, 8}) {
		t.Fatalf("gaps %v", gaps)
	}
	// A window entirely beyond the received set reports every number absent;
	// the caller only asks about windows up to its own high-water mark.
	if m, gaps := s.missing(20, 22); m != 3 || len(gaps) != 1 {
		t.Fatalf("missing beyond window %d %v", m, gaps)
	}
}

// TestSeqSetCoalescesBothSides covers a number that fills a one-wide gap: it
// must merge the two neighbouring ranges into one, not leave an adjacent pair.
func TestSeqSetCoalescesBothSides(t *testing.T) {
	var s seqSet
	for _, n := range []uint64{1, 3} {
		s.add(n)
	}
	if s.add(2) {
		t.Fatal("2 reported duplicate")
	}
	if len(s.ranges) != 1 || s.ranges[0] != (Range{1, 3}) {
		t.Fatalf("ranges not coalesced: %v", s.ranges)
	}
}

func TestTimestampNs(t *testing.T) {
	if got := timestampNs(90000, 90000); got != 1_000_000_000 {
		t.Fatalf("1s %d", got)
	}
	if got := timestampNs(48000, 48000); got != 1_000_000_000 {
		t.Fatalf("48k %d", got)
	}
	if got := timestampNs(1, 3); got != 333_333_333 {
		t.Fatalf("1/3 %d", got)
	}
}

func TestPaddingIsExcludedFromPayload(t *testing.T) {
	p := &pionrtp.Packet{
		Header:  pionrtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 3, SSRC: 4},
		Payload: []byte("abc"),
	}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// Set the padding bit and append a 2-byte pad whose last byte is the pad
	// count; the payload is still the three original bytes.
	dgram := append(raw, 0x00, 0x02)
	dgram[0] |= 0x20
	c, _ := Read(bytes.NewReader(capture(dgram)), "pad.rtpj", Options{})
	if len(c.Errors) != 0 {
		t.Fatalf("errors %v", c.Errors)
	}
	if got := c.Records[0].PayloadLen; got != 3 {
		t.Fatalf("payload len %d, want 3", got)
	}
}

func TestBadPaddingLengthIsTruncation(t *testing.T) {
	dgram := packet(1, 1, 0, "abc")
	dgram[0] |= 0x20
	dgram = append(dgram, 0x09) // claims nine pad bytes, only one present
	c, _ := Read(bytes.NewReader(capture(dgram)), "badpad.rtpj", Options{})
	if len(c.Records) != 0 || len(c.Errors) != 1 || c.Errors[0].Code != model.CodeRTPTruncated {
		t.Fatalf("records %d errors %v", len(c.Records), c.Errors)
	}
}
