package clock

import (
	"testing"

	"github.com/pion/rtp"
)

func TestUnwrapperWraparound(t *testing.T) {
	u := NewUnwrapper32()
	cases := []struct {
		raw uint32
		exp int64
	}{
		{0xFFFFFFF0, 0xFFFFFFF0},
		{0xFFFFFFF8, 0xFFFFFFF8},
		{0x00000000, 0x100000000},
		{0x00000008, 0x100000008},
		{0x00000010, 0x100000010},
	}
	for _, c := range cases {
		if got := u.Push(c.raw); got != c.exp {
			t.Fatalf("Push(%#x)=%d want %d", c.raw, got, c.exp)
		}
	}
	if u.WrapCount() != 1 {
		t.Fatalf("WrapCount=%d want 1", u.WrapCount())
	}
}

func TestUnwrapperBackwardWrap(t *testing.T) {
	u := NewUnwrapper32()
	got := []int64{u.Push(16), u.Push(0xFFFFFFF0)}
	// 16 -> 0xFFFFFFF0 is a backward wrap of -48 ticks.
	if got[0] != 16 || got[1] != 16-48 {
		t.Fatalf("backward wrap result=%v", got)
	}
	if u.WrapCount() != 1 {
		t.Fatalf("WrapCount=%d want 1", u.WrapCount())
	}
}

func TestExtendRTP(t *testing.T) {
	// Build RTP packets whose 32-bit timestamp crosses the wrap boundary.
	raws := []uint32{0xFFFFFFE0, 0xFFFFFFF0, 0x00000000, 0x00000010}
	pkts := make([][]byte, len(raws))
	for i, ts := range raws {
		p := &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: uint16(i), Timestamp: ts, SSRC: 42},
			Payload: []byte{0xAA, 0xBB},
		}
		buf, err := p.Marshal()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		pkts[i] = buf
	}
	unwrapped, raw, err := ExtendRTP(pkts)
	if err != nil {
		t.Fatalf("ExtendRTP: %v", err)
	}
	want := []int64{0xFFFFFFE0, 0xFFFFFFF0, 0x100000000, 0x100000010}
	for i := range want {
		if unwrapped[i] != want[i] {
			t.Fatalf("unwrapped[%d]=%d want %d", i, unwrapped[i], want[i])
		}
		if raw[i] != raws[i] {
			t.Fatalf("raw[%d]=%#x want %#x (raw must be preserved)", i, raw[i], raws[i])
		}
	}
}

func TestExtendRTPBadPacket(t *testing.T) {
	if _, _, err := ExtendRTP([][]byte{{0x00}}); err == nil {
		t.Fatal("expected error for truncated RTP packet")
	}
}

func TestEstimateDriftCFR(t *testing.T) {
	// Constant 3000-tick duration, perfectly linear timestamps: drift 0.
	durations := make([]uint32, 10)
	unwrapped := make([]int64, 10)
	for i := range durations {
		durations[i] = 3000
		unwrapped[i] = int64(i) * 3000
	}
	r := EstimateDrift(unwrapped, durations)
	if r.VFR {
		t.Fatal("CFR stream must not be flagged VFR")
	}
	if r.DriftPPM != 0 {
		t.Fatalf("DriftPPM=%d want 0", r.DriftPPM)
	}
}

func TestEstimateDriftVFRAndDrift(t *testing.T) {
	// Alternating durations (VFR) and timestamps running 0.1% fast.
	durations := []uint32{3000, 6000, 3000, 6000, 3000, 6000}
	unwrapped := make([]int64, len(durations))
	for i := 1; i < len(durations); i++ {
		unwrapped[i] = unwrapped[i-1] + int64(float64(durations[i-1])*1.001)
	}
	r := EstimateDrift(unwrapped, durations)
	if !r.VFR {
		t.Fatal("alternating durations must be flagged VFR")
	}
	if r.DriftPPM <= 0 {
		t.Fatalf("positive drift expected, got %d PPM", r.DriftPPM)
	}
	if r.DriftPPM < 900 || r.DriftPPM > 1100 {
		t.Fatalf("DriftPPM=%d want ~1000", r.DriftPPM)
	}
	if !DriftExceeded(1500, 1) || DriftExceeded(500, 1) {
		t.Fatal("DriftExceeded threshold logic wrong")
	}
}
