package ts_test

import (
	"bytes"
	"testing"

	"hlscheck/internal/demo"
	"hlscheck/internal/ts"
)

func TestPTSRoundTrip(t *testing.T) {
	for _, pts := range []int64{0, 1, 90000, 1 << 32, (1 << 33) - 1} {
		hdr := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 0x05}
		hdr = ts.EncodePTS(hdr, pts)
		h, err := ts.ParsePES(hdr)
		if err != nil {
			t.Fatalf("pts %d: %v", pts, err)
		}
		if h.PTS != pts {
			t.Errorf("pts %d decoded as %d", pts, h.PTS)
		}
		if h.PayloadOffset != 14 {
			t.Errorf("payload offset = %d, want 14", h.PayloadOffset)
		}
	}
}

func TestPTSUnwrap(t *testing.T) {
	var uw ts.PTSUnwrapper
	base := int64(1<<33) - 10*90000 // 10 s before wraparound
	if v := uw.Unwrap(base); v != base {
		t.Fatalf("first pts = %d", v)
	}
	if uw.Wrapped {
		t.Fatal("wrapped too early")
	}
	after := int64(5 * 90000) // 5 s past the boundary
	got := uw.Unwrap(after)
	if !uw.Wrapped {
		t.Fatal("wrap not detected")
	}
	if want := after + (1 << 33); got != want {
		t.Errorf("unwrapped = %d, want %d", got, want)
	}
}

func TestHasIDR(t *testing.T) {
	if !ts.HasIDR([]byte{0, 0, 0, 1, 0x65, 9, 9}) {
		t.Error("4-byte start code IDR not detected")
	}
	if !ts.HasIDR([]byte{0, 0, 1, 0x65, 9}) {
		t.Error("3-byte start code IDR not detected")
	}
	if ts.HasIDR([]byte{0, 0, 0, 1, 0x41, 9}) {
		t.Error("non-IDR misdetected")
	}
	if ts.HasIDR([]byte{1, 2, 3, 4}) {
		t.Error("random payload misdetected")
	}
}

func TestScanPTSGenerated(t *testing.T) {
	var buf bytes.Buffer
	err := demo.Generate(&buf, demo.GenOptions{BitrateKbps: 450, DurationSec: 4, GOPSec: 2, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	first, last, count, anomalies, err := ts.ScanPTS(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(anomalies) != 0 {
		t.Errorf("anomalies = %v", anomalies)
	}
	if count != 100 { // 4 s at 25 fps
		t.Errorf("count = %d, want 100", count)
	}
	start := int64(ts.ClockRate * 3600)
	if first != start || last != start+99*3600 {
		t.Errorf("first=%d last=%d, want %d..%d", first, last, start, start+99*3600)
	}
}
