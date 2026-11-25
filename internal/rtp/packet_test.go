package rtp

import (
	"errors"
	"testing"

	"github.com/pion/rtp"

	"tracecheck/internal/model"
)

func TestParseRTPRoundTrip(t *testing.T) {
	original := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: 4242,
			Timestamp:      90000,
			SSRC:           7,
			CSRC:           []uint32{1, 2},
		},
		Payload: []byte{0xDE, 0xAD, 0xBE, 0xEF},
	}
	datagram, err := original.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseRTP(datagram)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Header.SequenceNumber != 4242 || parsed.Header.Timestamp != 90000 {
		t.Fatalf("header fields wrong: %+v", parsed.Header)
	}
	if len(parsed.Header.CSRC) != 2 {
		t.Fatal("CSRC list must survive parsing")
	}
	if string(parsed.Payload) != string(original.Payload) {
		t.Fatalf("payload wrong: %x", parsed.Payload)
	}
}

func TestParseRTPRejectsMalformed(t *testing.T) {
	if _, err := ParseRTP([]byte{0x80, 0x00}); !errors.Is(err, ErrRTPHeader) {
		t.Fatalf("short datagram: %v", err)
	}
	// Version 1 is illegal.
	bad := []byte{0x40, 0x60, 0x00, 0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0xAA}
	if _, err := ParseRTP(bad); !errors.Is(err, ErrRTPHeader) {
		t.Fatalf("bad version: %v", err)
	}
}

func TestRecordFromDatagram(t *testing.T) {
	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: 99, Timestamp: 1234, SSRC: 5},
		Payload: []byte{1, 2, 3},
	}
	datagram, _ := pkt.Marshal()
	rec, err := RecordFromDatagram(5, model.CodecOpus, datagram, 777)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Sequence != 99 || rec.RTPTimestamp != 1234 || rec.ArrivalTick != 777 {
		t.Fatalf("record wrong: %+v", rec)
	}
	if len(rec.Payload) != 3 {
		t.Fatalf("payload wrong: %x", rec.Payload)
	}
	if _, err := RecordFromDatagram(5, model.CodecOpus, []byte{1, 2}, 0); err == nil {
		t.Fatal("malformed datagram must error")
	}
}
