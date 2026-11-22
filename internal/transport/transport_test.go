package transport

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func pkt(pid uint16, cc uint8) *Packet {
	return &Packet{PID: pid, HasPayload: true, ContinuityCounter: cc}
}

func TestContinuityMissing(t *testing.T) {
	ct := NewContinuityTracker()
	ct.Observe(pkt(256, 0))
	ct.Observe(pkt(256, 2)) // gap of one
	ct.Observe(pkt(256, 3))
	if ct.Totals.Missing != 1 || ct.Totals.Duplicates != 0 {
		t.Fatalf("totals: %+v", ct.Totals)
	}
	if ct.ByPID[256].Missing != 1 {
		t.Fatalf("per-PID: %+v", ct.ByPID[256])
	}
}

func TestContinuityDuplicate(t *testing.T) {
	ct := NewContinuityTracker()
	ct.Observe(pkt(256, 0))
	ct.Observe(pkt(256, 0)) // repeated packet
	ct.Observe(pkt(256, 1))
	if ct.Totals.Duplicates != 1 || ct.Totals.Missing != 0 {
		t.Fatalf("totals: %+v", ct.Totals)
	}
}

func TestContinuityDiscontinuityResetsBaseline(t *testing.T) {
	ct := NewContinuityTracker()
	ct.Observe(pkt(256, 0))
	d := pkt(256, 7)
	d.DiscontinuityIndicator = true
	ct.Observe(d)
	ct.Observe(pkt(256, 8))
	if ct.Totals.Discontinuities != 1 || ct.Totals.Missing != 0 {
		t.Fatalf("totals: %+v", ct.Totals)
	}
}

func TestContinuityAdaptationOnlyKeepsCounter(t *testing.T) {
	ct := NewContinuityTracker()
	ct.Observe(pkt(256, 0))
	ct.Observe(&Packet{PID: 256, HasPayload: false, ContinuityCounter: 9})
	ct.Observe(pkt(256, 1))
	if ct.Totals.Missing != 0 {
		t.Fatalf("totals: %+v", ct.Totals)
	}
}

func TestContinuityTransportErrorIgnored(t *testing.T) {
	ct := NewContinuityTracker()
	ct.Observe(pkt(256, 0))
	bad := pkt(256, 5)
	bad.TransportError = true
	ct.Observe(bad)
	ct.Observe(pkt(256, 1))
	if ct.Totals.Missing != 0 {
		t.Fatalf("totals: %+v", ct.Totals)
	}
}

// rawPacket builds a minimal 188-byte packet for reader tests.
func rawPacket(pid uint16, cc uint8) []byte {
	b := make([]byte, PacketSize)
	b[0] = SyncByte
	b[1] = byte(pid >> 8)
	b[2] = byte(pid)
	b[3] = 0x10 | cc
	for i := 4; i < len(b); i++ {
		b[i] = 0xFF
	}
	return b
}

func TestReaderCleanStream(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(rawPacket(100, 0))
	buf.Write(rawPacket(100, 1))
	r := NewReader(&buf, false)
	for i, want := range []uint8{0, 1} {
		p, err := r.Next()
		if err != nil || p.ContinuityCounter != want || p.Index != i {
			t.Fatalf("packet %d: %+v err=%v", i, p, err)
		}
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestReaderSyncErrorStrict(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(rawPacket(100, 0))
	bad := rawPacket(100, 1)
	bad[0] = 0x00
	buf.Write(bad)
	r := NewReader(&buf, false)
	if _, err := r.Next(); err != nil {
		t.Fatalf("first packet: %v", err)
	}
	if _, err := r.Next(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestReaderSyncErrorTolerant(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(rawPacket(100, 0))
	bad := rawPacket(100, 1)
	bad[0] = 0x00
	buf.Write(bad)
	buf.Write(rawPacket(100, 2))
	r := NewReader(&buf, true)
	count := 0
	for {
		_, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tolerant read: %v", err)
		}
		count++
	}
	if count != 2 || r.Stats().SyncErrors != 1 {
		t.Fatalf("count=%d stats=%+v", count, r.Stats())
	}
}

func TestReaderTruncatedTail(t *testing.T) {
	data := append(rawPacket(100, 0), make([]byte, 50)...)
	r := NewReader(bytes.NewReader(data), false)
	if _, err := r.Next(); err != nil {
		t.Fatalf("first packet: %v", err)
	}
	if _, err := r.Next(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
	rt := NewReader(bytes.NewReader(data), true)
	for {
		_, err := rt.Next()
		if err == io.EOF {
			break
		}
	}
	if rt.Stats().TruncatedTailBytes != 50 {
		t.Fatalf("stats: %+v", rt.Stats())
	}
}
