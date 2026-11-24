package ingest

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/pion/rtp"
)

// buildStream concatenates framed records into one byte stream.
func buildStream(records ...[]byte) []byte {
	var buf bytes.Buffer
	for _, rec := range records {
		buf.Write(rec)
	}
	return buf.Bytes()
}

func frame(tag byte, payload []byte) []byte {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, tag, payload); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func validRTP(t *testing.T, seq uint16, ts uint32, payload []byte) []byte {
	t.Helper()
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           0x42424242,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func asParseErr(t *testing.T, err error) *ParseError {
	t.Helper()
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("want *ParseError, got %T: %v", err, err)
	}
	return pe
}

func TestReaderHappyPath(t *testing.T) {
	raw1 := validRTP(t, 7, 1000, []byte{1, 2, 3, 4})
	raw2 := validRTP(t, 8, 1048, []byte{9})
	stream := buildStream(frame(TagRTP, raw1), frame(TagRTP, raw2))
	rd := NewReader(bytes.NewReader(stream))

	f1, err := rd.Next()
	if err != nil {
		t.Fatalf("frame1: %v", err)
	}
	if f1.FrameOffset != 0 || f1.PayloadOffset != 5 {
		t.Fatalf("frame1 offsets = %d/%d, want 0/5", f1.FrameOffset, f1.PayloadOffset)
	}
	p1, err := ParseRTP(f1)
	if err != nil {
		t.Fatalf("parse1: %v", err)
	}
	if p1.Packet.SequenceNumber != 7 || p1.Offset != 0 {
		t.Fatalf("p1 = %+v", p1)
	}

	f2, err := rd.Next()
	if err != nil {
		t.Fatalf("frame2: %v", err)
	}
	wantOff := int64(5 + len(raw1))
	if f2.FrameOffset != wantOff {
		t.Fatalf("frame2 offset = %d, want %d", f2.FrameOffset, wantOff)
	}
	if _, err := rd.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestTruncatedLengthAndPayload(t *testing.T) {
	// Truncated length header: tag + only two of four length bytes.
	rd := NewReader(bytes.NewReader([]byte{TagRTP, 0x00, 0x01}))
	_, err := rd.Next()
	pe := asParseErr(t, err)
	if pe.Code != ErrTruncated || pe.Offset != 0 || pe.Stage != StageParse {
		t.Fatalf("length truncation = %+v", pe)
	}

	// Declared length exceeds available payload: exact offending offset.
	good := frame(TagRTP, validRTP(t, 1, 10, []byte{0}))
	bad := []byte{TagRTP, 0x00, 0x00, 0x10, 0x00, 0xaa, 0xbb}
	rd = NewReader(bytes.NewReader(buildStream(good, bad)))
	if _, err := rd.Next(); err != nil {
		t.Fatalf("good frame: %v", err)
	}
	_, err = rd.Next()
	pe = asParseErr(t, err)
	wantOff := int64(len(good))
	if pe.Code != ErrTruncated || pe.Offset != wantOff {
		t.Fatalf("payload truncation = %+v, want offset %d", pe, wantOff)
	}
}

func TestMalformedLengthAndOversize(t *testing.T) {
	// Zero length.
	rd := NewReader(bytes.NewReader([]byte{TagRTP, 0, 0, 0, 0}))
	_, err := rd.Next()
	if pe := asParseErr(t, err); pe.Code != ErrBadLength || pe.Offset != 0 {
		t.Fatalf("zero length = %+v", pe)
	}

	// Length above 64 KiB.
	rd = NewReader(bytes.NewReader([]byte{TagRTP, 0x00, 0x01, 0x00, 0x01}))
	_, err = rd.Next()
	if pe := asParseErr(t, err); pe.Code != ErrPayloadTooLarge {
		t.Fatalf("oversize = %+v", pe)
	}

	// Unknown tag byte.
	rd = NewReader(bytes.NewReader([]byte{0x7f, 0, 0, 0, 1, 0}))
	_, err = rd.Next()
	if pe := asParseErr(t, err); pe.Code != ErrUnknownFrame || pe.Offset != 0 {
		t.Fatalf("unknown tag = %+v", pe)
	}
}

func TestRTPParseErrorOffset(t *testing.T) {
	// RTP fixed header needs 12 bytes; feed 6.
	f := &Frame{Type: TagRTP, FrameOffset: 123, Payload: []byte{0x80, 0x60, 0, 1, 0, 0}}
	_, err := ParseRTP(f)
	pe := asParseErr(t, err)
	if pe.Code != ErrRTPHeader || pe.Offset != 123 || pe.Stage != StageParse {
		t.Fatalf("rtp parse = %+v", pe)
	}
}

func TestRTCPParse(t *testing.T) {
	// Build one SR: v=2, p=0, rc=0, PT=200, length=6 (28 bytes).
	sr := make([]byte, 28)
	sr[0] = 0x80
	sr[1] = typeSenderReport
	sr[2], sr[3] = 0x00, 0x06 // length in 32-bit words minus one
	sr[4] = 0x11
	// NTP = 0x00000001_00000000 (one second), RTP = 7777
	sr[8], sr[9], sr[10], sr[11] = 0, 0, 0, 1
	sr[16], sr[17], sr[18], sr[19] = 0, 0, 0x1e, 0x61
	stream := frame(TagRTCP, sr)
	rd := NewReader(bytes.NewReader(stream))
	f, err := rd.Next()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	pkt, err := ParseRTCP(f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pkt.Reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(pkt.Reports))
	}
	got := pkt.Reports[0]
	if got.NTPTime != 1<<32 || got.RTPTime != 7777 || got.SSRC != 0x11000000 {
		t.Fatalf("sr = %+v", got)
	}

	// Block claiming to run past the frame end.
	bad := make([]byte, 28)
	bad[0] = 0x80
	bad[1] = typeSenderReport
	bad[2], bad[3] = 0x00, 0x10 // declares 68 bytes
	_, err = ParseRTCP(&Frame{Type: TagRTCP, FrameOffset: 55, Payload: bad})
	pe := asParseErr(t, err)
	if pe.Code != ErrRTCPHeader || pe.Offset != 55 {
		t.Fatalf("rtcp bad length = %+v", pe)
	}
}
