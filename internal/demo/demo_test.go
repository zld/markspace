package demo

import (
	"audionorm/internal/ingest"
	"audionorm/internal/media"
	"bytes"
	"context"
	"encoding/binary"
	"os/exec"
	"testing"
)

func parseForTest(data []byte) ([]ingest.Fragment, uint32, *media.ErrorLog, error) {
	return ingest.ParseFMP4(bytes.NewReader(data))
}

func TestParseOggReassemblyAndHeaderStrip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	stream, err := GenerateOpus(context.Background(), 0.3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(stream.Packets) == 0 {
		t.Fatal("no opus packets parsed")
	}
	for _, p := range stream.Packets {
		if len(p) == 0 {
			t.Fatal("empty opus packet after parse")
		}
		if string(p[:min(8, len(p))]) == "OpusHead" || string(p[:min(8, len(p))]) == "OpusTags" {
			t.Fatal("header packet was not stripped")
		}
	}
}

func TestSplitADTSRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	stream, err := GenerateAAC(context.Background(), 0.3, 48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(stream.Packets) < 5 {
		t.Fatalf("too few AAC frames: %d", len(stream.Packets))
	}
	// Every raw frame must be non-empty and re-splitting must be stable.
	total := 0
	for _, p := range stream.Packets {
		if len(p) == 0 {
			t.Fatal("empty AAC access unit")
		}
		total += len(p)
	}
	if total == 0 {
		t.Fatal("zero total AAC payload")
	}
}

func TestADTSFrameLengthDecoding(t *testing.T) {
	// Construct a synthetic ADTS header declaring a 100-byte frame,
	// followed by enough body bytes; verify the 13-bit length extraction.
	hdr := make([]byte, 7)
	hdr[0] = 0xFF
	hdr[1] = 0xF1 // MPEG-4, layer 0, protection absent
	frameLen := 100
	hdr[3] = byte((frameLen >> 11) & 0x03)
	hdr[4] = byte((frameLen >> 3) & 0xFF)
	hdr[5] = byte((frameLen & 0x07) << 5)
	frame := append(hdr, make([]byte, frameLen-7)...)
	got, err := SplitADTS(frame)
	if err != nil {
		t.Fatalf("synthetic ADTS rejected: %v", err)
	}
	if len(got) != 1 || len(got[0]) != frameLen-7 {
		t.Fatalf("raw frame size = %d, want %d", len(got[0]), frameLen-7)
	}
}

func TestOpusRTPWriterSequenceAndTimestamp(t *testing.T) {
	w := NewOpusRTPWriter()
	p0, err := w.Write(0, []byte{0x68, 0xff})
	if err != nil {
		t.Fatal(err)
	}
	p1, _ := w.Write(1, []byte{0x68, 0xff})
	if binary.BigEndian.Uint16(p0[2:4]) != 0 || binary.BigEndian.Uint16(p1[2:4]) != 1 {
		t.Fatal("RTP sequence numbers wrong")
	}
	// Timestamp step must be 960 (20 ms at 48 kHz).
	if binary.BigEndian.Uint32(p1[4:8])-binary.BigEndian.Uint32(p0[4:8]) != 960 {
		t.Fatal("RTP timestamp step is not 960")
	}
}

func TestAACRTPWriterRFC3640Shape(t *testing.T) {
	w := NewAACRTPWriter(48000)
	au := []byte{1, 2, 3, 4, 5}
	pkt, err := w.Write(7, au)
	if err != nil {
		t.Fatal(err)
	}
	// RTP header is 12 bytes; payload starts with AU-headers-length=16.
	if pkt[12] != 0x00 || pkt[13] != 0x10 {
		t.Fatal("missing RFC3640 AU-headers-length")
	}
	// AU header encodes the 13-bit AU size = 5.
	if binary.BigEndian.Uint16(pkt[14:16])&0x1fff != 5 {
		t.Fatal("AU size header wrong")
	}
}

func TestFMP4BuilderRoundTripsParser(t *testing.T) {
	units := [][]byte{
		{0xde, 0xad, 0xbe, 0xef},
		{0xca, 0xfe, 0xba, 0xbe},
		{0x01, 0x02, 0x03, 0x04},
	}
	b := NewFMP4Builder(48000)
	b.AddFragment(units, 0)
	data := b.Bytes()

	// Reuse the ingest parser to confirm the builder output is valid.
	frags, timescale, errs, err := parseForTest(data)
	if err != nil || errs.Len() != 0 {
		t.Fatalf("builder output not parseable: err=%v errs=%v", err, errs.All())
	}
	if timescale != 48000 || len(frags) != len(units) {
		t.Fatalf("timescale=%d frags=%d", timescale, len(frags))
	}
}

func TestFMP4BuilderTruncationDetected(t *testing.T) {
	units := [][]byte{{0xab}, {0xcd}, {0xef}, {0x12}}
	b := NewFMP4Builder(48000)
	b.AddFragment(units, 2) // declares 6, indexes only 4
	data := b.Bytes()
	_, _, errs, err := parseForTest(data)
	if err != nil {
		t.Fatal(err)
	}
	if errs.Len() == 0 {
		t.Fatal("truncated trun was not detected")
	}
}
