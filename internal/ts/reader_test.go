package ts_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"hlscheck/internal/ts"
)

// pkt builds one 188-byte packet with a payload-only adaptation ctrl.
func pkt(pid uint16, pusi bool, cc int, payload []byte) []byte {
	p := make([]byte, ts.PacketSize)
	p[0] = ts.SyncByte
	p[1] = byte(pid >> 8)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	p[3] = 0x10 | byte(cc)
	copy(p[4:], payload)
	return p
}

func TestNextPackets(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(pkt(0x101, true, 0, []byte{1, 2, 3}))
	buf.Write(pkt(0x101, false, 1, []byte{4, 5, 6}))
	rd := ts.NewReader(&buf)
	p1, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	if p1.PID != 0x101 || !p1.PUSI || p1.Offset != 0 || p1.Payload[0] != 1 {
		t.Errorf("bad first packet: %+v", p1)
	}
	p2, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	if p2.Offset != 188 || p2.CC != 1 {
		t.Errorf("bad second packet: %+v", p2)
	}
	if _, err := rd.Next(); err != io.EOF {
		t.Errorf("want io.EOF, got %v", err)
	}
	if len(rd.Anomalies()) != 0 {
		t.Errorf("unexpected anomalies: %v", rd.Anomalies())
	}
}

func TestTruncatedPacket(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(pkt(0x101, false, 0, nil))
	buf.Write(make([]byte, 100)) // partial packet
	rd := ts.NewReader(&buf)
	if _, err := rd.Next(); err != nil {
		t.Fatal(err)
	}
	_, err := rd.Next()
	var te *ts.TruncatedError
	if !errors.As(err, &te) {
		t.Fatalf("want TruncatedError, got %v", err)
	}
	if te.Offset != 188 || te.Have != 100 {
		t.Errorf("truncated at %d have %d, want 188/100", te.Offset, te.Have)
	}
}

func TestSyncErrorAndResync(t *testing.T) {
	// Unrecoverable: no valid sync pattern at all.
	rd := ts.NewReader(bytes.NewReader(make([]byte, 500)))
	var se *ts.SyncError
	if _, err := rd.Next(); !errors.As(err, &se) {
		t.Fatalf("want SyncError, got %v", err)
	}
	// Recoverable: 5 stray bytes then valid packets.
	var buf bytes.Buffer
	buf.Write([]byte{9, 9, 9, 9, 9})
	buf.Write(pkt(0x101, false, 0, nil))
	buf.Write(pkt(0x101, false, 1, nil))
	rd = ts.NewReader(&buf)
	p, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	if p.Offset != 5 {
		t.Errorf("resync offset = %d, want 5", p.Offset)
	}
	an := rd.Anomalies()
	if len(an) != 1 || an[0].Kind != "resync" || an[0].Offset != 0 {
		t.Errorf("anomalies = %v", an)
	}
}

func TestContinuityJump(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(pkt(0x101, false, 0, []byte{1}))
	buf.Write(pkt(0x101, false, 2, []byte{1})) // skip cc=1
	rd := ts.NewReader(&buf)
	for {
		if _, err := rd.Next(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	an := rd.Anomalies()
	if len(an) != 1 || an[0].Kind != "cc_jump" || an[0].Offset != 188 {
		t.Fatalf("anomalies = %v", an)
	}
}
