package stream

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/pion/rtp"

	"mediapipe/internal/model"
)

func buildTestStream(t *testing.T, packets ...*rtp.Packet) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, 8000)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range packets {
		if _, err := w.WriteRTP(p); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func TestReaderRoundTrip(t *testing.T) {
	raw := buildTestStream(t, &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: 7, Timestamp: 160, SSRC: 9},
		Payload: []byte("hello-media"),
	})
	h, recs, err := ReadAll(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if h.ClockRate != 8000 || len(recs) != 1 {
		t.Fatalf("header=%+v records=%d", h, len(recs))
	}
	if recs[0].RTPPacket.SequenceNumber != 7 || string(recs[0].RTPPacket.Payload) != "hello-media" {
		t.Fatalf("bad round trip: %+v", recs[0].RTPPacket)
	}
}

func TestReaderBadHeaderIsClassified(t *testing.T) {
	bad := make([]byte, FileHeaderSize)
	copy(bad[0:4], "NOPE")
	_, err := ReadFileHeader(bytes.NewReader(bad))
	if !model.IsClass(err, model.ErrorHeader) {
		t.Fatalf("error = %v, want header class", err)
	}
	var me *model.Error
	if !errors.As(err, &me) || me.Offset != 0 {
		t.Fatalf("structured offset error = %+v", err)
	}
}

func TestReaderTruncationHasOffsetAndKeepsPriorPacket(t *testing.T) {
	raw := buildTestStream(t,
		&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 1}, Payload: []byte("one")},
		&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: 2}, Payload: make([]byte, 64)},
	)
	cut := raw[:len(raw)-5]
	r, _, err := NewReader(bytes.NewReader(cut))
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.Next()
	if err != nil || first.RTPPacket.SequenceNumber != 1 {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	_, err = r.Next()
	if !model.IsClass(err, model.ErrorTruncated) {
		t.Fatalf("second err = %v, want truncation", err)
	}
	var me *model.Error
	if errors.As(err, &me) && me.Offset <= 0 {
		t.Fatalf("truncation offset should be positive: %+v", me)
	}
}

func TestReaderCorruptCRCRecordIsContinueable(t *testing.T) {
	raw := buildTestStream(t, &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: 9},
		Payload: bytes.Repeat([]byte{0x5a}, 16),
	})
	corrupt := append([]byte(nil), raw...)
	payloadStart := FileHeaderSize + RecordHeaderSize
	corrupt[payloadStart+15] ^= 0xff

	_, recs, err := ReadAll(bytes.NewReader(corrupt))
	if err != nil && !model.IsClass(err, model.ErrorCorrupt) {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	if !recs[0].Corrupt || recs[0].CRCError == nil {
		t.Fatalf("record not marked corrupt: %+v", recs[0])
	}
}

func TestReaderCleanEOF(t *testing.T) {
	raw := buildTestStream(t)
	r, _, err := NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
}
