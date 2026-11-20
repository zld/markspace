package ts

import (
	"bytes"
	"errors"
	"testing"

	"hlscheck/internal/model"
)

// TestDemuxerMalformedStreamLocatesOffset feeds the demuxer a stream whose
// second packet has a destroyed sync byte and asserts the error surfaces the
// absolute byte offset rather than a generic read failure.
func TestDemuxerMalformedStreamLocatesOffset(t *testing.T) {
	raw := make([]byte, model.PacketSize*3)
	for i := range raw {
		if i%model.PacketSize == 0 {
			raw[i] = model.SyncByte
		}
	}
	// Corrupt the sync byte of the packet at offset 376 (2 * 188).
	raw[2*model.PacketSize] = 0x00
	d := NewDemuxer(bytes.NewReader(raw))
	for d.Scan() {
	}
	if !errors.Is(d.LastErr, ErrSyncByte) {
		t.Fatalf("err = %v, want ErrSyncByte", d.LastErr)
	}
	if !bytes.Contains([]byte(d.LastErr.Error()), []byte("offset 376")) {
		t.Fatalf("error %q does not locate offset 376", d.LastErr.Error())
	}
}

// TestDemuxerTruncatedStream reports the short-tail condition explicitly.
func TestDemuxerTruncatedStreamReportsShortPacket(t *testing.T) {
	raw := make([]byte, model.PacketSize+40)
	raw[0] = model.SyncByte
	d := NewDemuxer(bytes.NewReader(raw))
	for d.Scan() {
	}
	if !errors.Is(d.LastErr, ErrShortPacket) {
		t.Fatalf("err = %v, want ErrShortPacket", d.LastErr)
	}
}

// TestCCErrorRingIsBounded builds a raw packet stream with many undeclared
// counter jumps on one PID and drives it through the real Scan loop, then
// confirms the retained sample is capped while the total keeps counting.
func TestCCErrorRingIsBounded(t *testing.T) {
	const count = maxRetainedCCErrors + 10
	raw := make([]byte, 0, model.PacketSize*count)
	for i := 0; i < count; i++ {
		pkt := make([]byte, model.PacketSize)
		pkt[0] = model.SyncByte
		// Fixed PID 0x0200, payload-only (adaptation control = 1 in bits),
		// counter jumps by two each time to force an undeclared CC jump.
		pkt[1] = 0x20 // high PID bits
		pkt[2] = 0x00
		pkt[3] = byte(0x10 | ((i * 2) & 0x0F))
		raw = append(raw, pkt...)
	}
	d := NewDemuxer(bytes.NewReader(raw))
	for d.Scan() {
	}
	if len(d.CCErrors()) > maxRetainedCCErrors {
		t.Fatalf("retained = %d, want cap %d", len(d.CCErrors()), maxRetainedCCErrors)
	}
	if d.CCTotal() < maxRetainedCCErrors {
		t.Fatalf("total = %d, want at least the retention window", d.CCTotal())
	}
	if d.CCTotal() != count-1 {
		// The first packet establishes state; every later packet is a jump.
		t.Fatalf("total = %d, want %d", d.CCTotal(), count-1)
	}
}
