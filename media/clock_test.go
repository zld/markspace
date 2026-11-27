package media

import (
	"testing"

	"github.com/pion/rtp"
)

func TestScaleExact(t *testing.T) {
	// 90000 ticks at 90 kHz == exactly 1 second.
	got, err := Scale(90000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1_000_000_000 {
		t.Fatalf("got %d ns, want 1e9", got)
	}
}

func TestScaleNoFrameDrift(t *testing.T) {
	// 30 frames of 3003 ticks at 90 kHz (29.97 fps): the cumulative
	// conversion must be exact, while naive per-frame truncation drifts.
	// The kernel therefore always converts cumulative tick counts.
	perFrame, err := Scale(3003, 90000)
	if err != nil {
		t.Fatal(err)
	}
	total, err := Scale(3003*30, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1_001_000_000 {
		t.Fatalf("cumulative total %d ns, want exact 1001000000", total)
	}
	if perFrame*30 == total {
		t.Fatal("per-frame truncation should drift; test premise broken")
	}
}

func TestScaleOverflowFails(t *testing.T) {
	// 2^63 ticks at 1 Hz cannot fit in int64 nanoseconds.
	if _, err := Scale(1<<63, 1); !IsKind(err, KindOverflow) {
		t.Fatalf("want overflow error, got %v", err)
	}
}

func TestScaleZeroTimescaleFails(t *testing.T) {
	if _, err := Scale(1, 0); !IsKind(err, KindBadTimescale) {
		t.Fatalf("want bad-timescale error, got %v", err)
	}
}

func TestTickAndWithinTick(t *testing.T) {
	// gcd(90000, 48000) = 6000 -> tick = 1e9/6000 ns ~ 166666.67 ns.
	ok, err := WithinTick(166666, 90000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("166666 ns should be within one tick")
	}
	ok, err = WithinTick(166667, 90000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("166667 ns should exceed one tick")
	}
}

// TestExtendTimestampRTPWrap feeds RTP packets across the 32-bit timestamp
// wrap boundary and checks the extended timeline stays monotonic.
func TestExtendTimestampRTPWrap(t *testing.T) {
	start := uint32(0xfffffff0)
	var prev uint64
	for i := 0; i < 64; i++ {
		pkt := &rtp.Packet{
			Header:  rtp.Header{SequenceNumber: uint16(1000 + i)},
			Payload: []byte{0x01},
		}
		pkt.Timestamp = start + uint32(i)*160 // crosses 2^32 mid-run
		raw, err := pkt.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var back rtp.Packet
		if err := back.Unmarshal(raw); err != nil {
			t.Fatal(err)
		}
		var ext uint64
		if i == 0 {
			ext = uint64(back.Timestamp)
		} else {
			ext = ExtendTimestamp(prev, back.Timestamp)
			if ext <= prev {
				t.Fatalf("sample %d: extended time went backwards (%d <= %d)", i, ext, prev)
			}
			if ext-prev != 160 {
				t.Fatalf("sample %d: delta %d, want 160", i, ext-prev)
			}
		}
		prev = ext
	}
	if prev < 1<<32 {
		t.Fatalf("extended timeline %d never crossed 2^32", prev)
	}
}
