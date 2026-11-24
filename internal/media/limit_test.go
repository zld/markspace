package media

import (
	"testing"

	"mediasync/internal/clock"
)

func TestRejectsOversizeInput(t *testing.T) {
	// A real 2 GiB+ slice is impractical in a unit test; instead verify the
	// guard constant and the RIFF-declared-size overflow path.
	if MaxInputSize != 2<<30 {
		t.Fatalf("MaxInputSize=%d want 2GiB", MaxInputSize)
	}
	if MaxIndexRecords != 5_000_000 {
		t.Fatalf("MaxIndexRecords=%d want 5m", MaxIndexRecords)
	}
	raw := buildWAV(2, 48000, 16, 4, 0)
	// Corrupt the RIFF size field to declare more than the 2 GiB limit.
	putU32(raw[4:8], uint32(0xFFFFFFFF))
	_, err := ParseWAV(raw)
	if err == nil {
		t.Fatal("oversized RIFF declaration must be rejected")
	}
	me := clock.AsMediaError(err)
	if me.Code != clock.ErrOverflow && me.Code != clock.ErrTruncated {
		t.Fatalf("expected overflow/truncation, got %s", me.Code)
	}
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}
