package ts

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// buildPacket assembles one TS packet with a simple payload-only adaptation.
func buildPacket(pid uint16, pusi bool, cc uint8, payload []byte) []byte {
	b := make([]byte, PacketSize)
	b[0] = 0x47
	if pusi {
		b[1] = 0x40
	}
	b[1] |= byte(pid >> 8)
	b[2] = byte(pid)
	b[3] = 0x10 | cc // payload only
	copy(b[4:], payload)
	return b
}

func patSection() []byte {
	// pointer byte + 16-byte section (section_length = 13).
	s := make([]byte, 17)
	s[1] = 0x00 // table id
	s[2], s[3] = 0xB0, 0x0D
	s[4], s[5] = 0x00, 0x01
	s[6] = 0xC1
	s[7], s[8] = 0x00, 0x00
	// program 1 -> PMT pid 0x1000 (reserved bits set in high byte: 0xF0).
	s[9], s[10] = 0x00, 0x01
	s[11], s[12] = 0xF0, 0x00
	return s
}

func pmtSection() []byte {
	// pointer byte + 26-byte section (section_length = 23).
	s := make([]byte, 27)
	s[1] = 0x02
	s[2], s[3] = 0xB0, 0x17
	s[4], s[5] = 0x00, 0x01
	s[6] = 0xC1
	s[7], s[8] = 0x00, 0x00
	s[9], s[10] = 0xE1, 0x00 // PCR PID 0x1000
	s[11], s[12] = 0xF0, 0x00
	// video H.264 on 0x0100, audio AAC on 0x0101
	s[13] = StreamH264
	s[14], s[15] = 0x01, 0x00
	s[16], s[17] = 0xF0, 0x00
	s[18] = StreamAACADTS
	s[19], s[20] = 0x01, 0x01
	s[21], s[22] = 0xF0, 0x00
	return s
}

func TestParseAndPSI(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(buildPacket(PIDPAT, true, 0, patSection()))
	buf.Write(buildPacket(0x1000, true, 0, pmtSection()))
	buf.Write(buildPacket(0x0100, true, 0, make([]byte, 100)))

	p := NewParser(&buf)
	for {
		_, err := p.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if err := p.ValidatePSI(); err != nil {
		t.Fatal(err)
	}
	if p.VideoPID() != 0x0100 || p.AudioPID() != 0x0101 {
		t.Fatalf("pids video=%#x audio=%#x", p.VideoPID(), p.AudioPID())
	}
	if p.Elementary[0x0100] != StreamH264 {
		t.Fatalf("stream type = %#x", p.Elementary[0x0100])
	}
}

func TestSyncAndTruncation(t *testing.T) {
	bad := buildPacket(0x100, false, 0, nil)
	bad[0] = 0x00
	if _, err := ParsePacket(bad); !errors.Is(err, ErrSyncByte) {
		t.Fatalf("got %v", err)
	}
	if _, err := ParsePacket(make([]byte, 7)); !errors.Is(err, ErrTruncated) {
		t.Fatalf("got %v", err)
	}
	// Streaming parser: corrupted sync byte, then a trailing partial packet.
	corrupt := buildPacket(0x100, false, 0, nil)
	corrupt[0] = 0x00
	p := NewParser(bytes.NewReader(corrupt))
	if _, err := p.Read(); !errors.Is(err, ErrSyncByte) {
		t.Fatalf("got %v", err)
	}
	p2 := NewParser(bytes.NewReader([]byte{0x47, 0x01}))
	if _, err := p2.Read(); !errors.Is(err, ErrTruncated) {
		t.Fatalf("got %v", err)
	}
	// Clean boundary reports plain io.EOF.
	p3 := NewParser(bytes.NewReader(buildPacket(0x100, false, 0, nil)))
	if _, err := p3.Read(); err != nil {
		t.Fatalf("got %v", err)
	}
	if _, err := p3.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v", err)
	}
}

func TestContinuityCounter(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(buildPacket(0x200, false, 0, nil))
	buf.Write(buildPacket(0x200, false, 2, nil)) // jump 0 -> 2
	p := NewParser(&buf)
	if _, err := p.Read(); err != nil {
		t.Fatal(err)
	}
	_, err := p.Read()
	var cc CCError
	if !errors.As(err, &cc) || cc.Expected != 1 || cc.Got != 2 {
		t.Fatalf("got %v", err)
	}
}

func TestParsePES(t *testing.T) {
	// Minimal PES with PTS=90000 (1s), header length 5.
	hdr := []byte{0x00, 0x00, 0x01, 0xE0, 0x00, 0x00, 0x80, 0x80, 0x05,
		0x21, 0x00, 0x05, 0xBF, 0x21, 0x00}
	info, err := ParsePES(hdr)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasPTS || info.PTS != 90000 {
		t.Fatalf("pts = %d has=%v", info.PTS, info.HasPTS)
	}
	if info.StreamID != 0xE0 {
		t.Fatalf("stream id %#x", info.StreamID)
	}
}
