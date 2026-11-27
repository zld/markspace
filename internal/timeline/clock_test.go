package timeline

import (
	"math"
	"testing"

	"github.com/pion/rtp"
)

func TestToNs(t *testing.T) {
	got, err := ToNs(90000, 90000, "src")
	if err != nil || got != 1_000_000_000 {
		t.Fatalf("ToNs(90000,90000) = %d, %v; want 1e9, nil", got, err)
	}
	got, err = ToNs(1, 2, "src")
	if err != nil || got != 500_000_000 {
		t.Fatalf("ToNs(1,2) = %d, %v; want 5e8, nil", got, err)
	}
}

func TestToNsZeroTimescale(t *testing.T) {
	_, err := ToNs(1, 0, "playlist.m3u8")
	te, ok := As(err)
	if !ok || te.Kind != KindZeroTimescale || te.Source != "playlist.m3u8" {
		t.Fatalf("want zero_timescale error with source, got %v", err)
	}
}

func TestToNsOverflow(t *testing.T) {
	_, err := ToNs(math.MaxUint64, 1, "manifest.mpd")
	te, ok := As(err)
	if !ok || te.Kind != KindOverflow {
		t.Fatalf("want overflow error, got %v", err)
	}
}

func TestMapperBackward(t *testing.T) {
	m := NewMapper()
	if err := m.Check("v1", 0, 1_000_000_000, "m.mpd", 0); err != nil {
		t.Fatalf("first interval rejected: %v", err)
	}
	if err := m.Check("v1", 1_000_000_000, 2_000_000_000, "m.mpd", 0); err != nil {
		t.Fatalf("adjacent interval rejected: %v", err)
	}
	err := m.Check("v1", 500_000_000, 1_500_000_000, "m.mpd", 7)
	te, ok := As(err)
	if !ok || te.Kind != KindBackward || te.Line != 7 {
		t.Fatalf("want backward_time error at line 7, got %v", err)
	}
	// other representations are independent
	if err := m.Check("v2", 0, 1, "m.mpd", 0); err != nil {
		t.Fatalf("independent representation rejected: %v", err)
	}
}

func TestRTPExtenderWrap(t *testing.T) {
	ext := &RTPExtender{}
	p1 := &rtp.Packet{Header: rtp.Header{SequenceNumber: 10, Timestamp: 0xFFFFFFFE}}
	if got := ext.Extend(p1); got != 0xFFFFFFFE {
		t.Fatalf("first extend = %#x", got)
	}
	p2 := &rtp.Packet{Header: rtp.Header{SequenceNumber: 11, Timestamp: 2}}
	if got := ext.Extend(p2); got != 1<<32+2 {
		t.Fatalf("wrapped extend = %#x, want %#x", got, uint64(1<<32+2))
	}
	// retransmitted packet (same sequence) must not bump the cycle
	p3 := &rtp.Packet{Header: rtp.Header{SequenceNumber: 11, Timestamp: 1}}
	if got := ext.Extend(p3); got != 1<<32+1 {
		t.Fatalf("retransmit extend = %#x", got)
	}
}
