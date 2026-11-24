package ts

import "testing"

// pesWithPTS builds a minimal PES payload carrying the given 33-bit PTS.
func pesWithPTS(streamID byte, pts uint64, withDTS bool) []byte {
	flags := byte(0x80) // marker bits
	if withDTS {
		flags |= 0xC0 // PTS+DTS indicator 0b11
	} else {
		flags |= 0x80 // PTS only indicator 0b10
	}
	payload := make([]byte, 188)
	payload[0], payload[1], payload[2] = 0x00, 0x00, 0x01
	payload[3] = streamID
	// PES packet length (placeholder), optional header follows.
	payload[6], payload[7] = 0x80, flags
	payload[8] = 5 // header data length
	b := payload[9:]
	b[0] = 0x21 | byte(pts>>29)&0x0e
	b[1] = byte(pts >> 22)
	b[2] = byte(pts>>14) & 0xfe
	b[3] = byte(pts >> 7)
	b[4] = byte(pts<<1) | 0x01
	return payload
}

func TestExtractPTS_Present(t *testing.T) {
	var want uint64 = 0x123456789
	p := &Packet{PayloadUnitStart: true, HasPayload: true,
		Payload: pesWithPTS(0xE0, want, false)}
	got, ok := p.ExtractPTS()
	if !ok || got != want {
		t.Fatalf("PTS = 0x%x ok=%v, want 0x%x", got, ok, want)
	}
}

func TestExtractPTS_PTSAndDTS(t *testing.T) {
	var want uint64 = 90000
	p := &Packet{PayloadUnitStart: true, HasPayload: true,
		Payload: pesWithPTS(0xC0, want, true)}
	got, ok := p.ExtractPTS()
	if !ok || got != want {
		t.Fatalf("PTS+DTS field: got 0x%x ok=%v", got, ok)
	}
}

func TestExtractPTS_NotPES(t *testing.T) {
	// PSI pointer-field payload is not a PES packet.
	psi := &Packet{PayloadUnitStart: true, HasPayload: true,
		Payload: append([]byte{0x00}, make([]byte, 187)...)}
	if _, ok := psi.ExtractPTS(); ok {
		t.Fatal("PSI payload must not yield a PTS")
	}
	// Non-start fragment has no PES header.
	frag := &Packet{PayloadUnitStart: false, HasPayload: true, Payload: make([]byte, 188)}
	if _, ok := frag.ExtractPTS(); ok {
		t.Fatal("continuation packet must not yield a PTS")
	}
	// Padding stream id 0xBE never carries PTS.
	pad := &Packet{PayloadUnitStart: true, HasPayload: true,
		Payload: pesWithPTS(0xBE, 123, false)}
	if _, ok := pad.ExtractPTS(); ok {
		t.Fatal("padding stream must not yield a PTS")
	}
}
