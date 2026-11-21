package tsreader

import (
	"bytes"
	"testing"
)

func TestPIDAndPayload(t *testing.T) {
	// b[0]=0x47 sync, b[1]=0x40 PUSI + PID high 0x00, b[2]=0x10 PID low ->
	// PID 0x0010, b[3]=0x10 payload-only flags + CC 0. Payload begins at b[4].
	var pkt [PacketSize]byte
	pkt[0], pkt[1], pkt[2], pkt[3] = 0x47, 0x40, 0x10, 0x10
	pkt[4] = 0x00 // pointer field
	pkt[5] = 0xAA
	r := NewReader(bytes.NewReader(pkt[:]))
	p, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if p.Header.PID != 0x0010 {
		t.Errorf("PID = %#04x, want 0x0010", p.Header.PID)
	}
	if !p.Header.PayloadUnitStartIndicator {
		t.Errorf("PUSI not set")
	}
	if !p.Header.HasPayload {
		t.Errorf("HasPayload not set")
	}
	if p.Header.ContinuityCounter != 0 {
		t.Errorf("CC = %d, want 0", p.Header.ContinuityCounter)
	}
	// pointer field stripped: payload starts at the section byte 0xAA.
	if len(p.Payload) == 0 || p.Payload[0] != 0xAA {
		t.Errorf("payload first byte = %v, want 0xAA (pointer stripped)", p.Payload)
	}
}

func TestTruncatedTail(t *testing.T) {
	// 188 good packets followed by 10 stray bytes.
	good := make([]byte, PacketSize)
	good[0] = SyncByte
	tail := make([]byte, 10)
	_, err := NewReader(bytes.NewReader(append(good, tail...))).Next()
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	r := NewReader(bytes.NewReader(append(good, tail...)))
	if _, err = r.Next(); err != nil {
		t.Fatalf("Next good: %v", err)
	}
	if _, err = r.Next(); err != ErrTruncated {
		t.Fatalf("Next tail err = %v, want ErrTruncated", err)
	}
	if r.TruncatedBytes() != 10 {
		t.Errorf("TruncatedBytes = %d, want 10", r.TruncatedBytes())
	}
}

func TestSyncError(t *testing.T) {
	var pkt [PacketSize]byte
	pkt[0] = 0x00 // bad sync
	r := NewReader(bytes.NewReader(pkt[:]))
	p, err := r.Next()
	if err != ErrSyncByte {
		t.Fatalf("err = %v, want ErrSyncByte", err)
	}
	if p == nil || p.SyncOK {
		t.Fatalf("damaged packet should still be delivered with SyncOK=false")
	}
}
