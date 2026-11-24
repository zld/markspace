package timeline_test

import (
	"testing"

	"github.com/pion/rtp"

	"mediaroute/internal/model"
	"mediaroute/internal/timeline"
)

func TestUnwrapRTPForwardWrap(t *testing.T) {
	// Three packets before the 2^32 boundary, three after.
	base := uint32(0xFFFFFF00)
	ts := []uint32{
		base, base + 0x40, base + 0x80,
		base + 0x80 + 0x40, // wraps in uint32 space
		base + 0x80 + 0x80,
	}
	unwrapped, wraps := timeline.UnwrapRTP(ts)
	if wraps != 1 {
		t.Fatalf("want 1 wrap, got %d", wraps)
	}
	for i := 1; i < len(unwrapped); i++ {
		if unwrapped[i] <= unwrapped[i-1] {
			t.Fatalf("unwrapped timeline not increasing at %d: %v", i, unwrapped)
		}
	}
	// The unwrapped deltas must equal the modular deltas.
	wantStep := uint64(0x40)
	for i := 1; i < len(unwrapped); i++ {
		if unwrapped[i]-unwrapped[i-1] != wantStep {
			t.Fatalf("step %d: got %d want %d", i, unwrapped[i]-unwrapped[i-1], wantStep)
		}
	}
}

func TestUnwrapRTPNoWrap(t *testing.T) {
	ts := []uint32{1000, 2000, 3000, 4000}
	unwrapped, wraps := timeline.UnwrapRTP(ts)
	if wraps != 0 {
		t.Fatalf("want no wraps, got %d", wraps)
	}
	if unwrapped[0] != 1000 || unwrapped[3] != 4000 {
		t.Fatalf("bad unwrap: %v", unwrapped)
	}
}

func TestUnwrapRTPBackwardStepClamps(t *testing.T) {
	ts := []uint32{1000, 900, 2000}
	unwrapped, _ := timeline.UnwrapRTP(ts)
	for i := 1; i < len(unwrapped); i++ {
		if unwrapped[i] < unwrapped[i-1] {
			t.Fatalf("unwrapped timeline regressed at %d", i)
		}
	}
}

func TestValidateRTPMatchesMicrosecondTimeline(t *testing.T) {
	const clock = timeline.RTPClockRate
	// Expected media times at 1s spacing.
	expected := []model.Micros{0, 1_000_000, 2_000_000}
	frames := []timeline.RTPFrame{
		{SequenceNumber: 1, Timestamp: 0},
		{SequenceNumber: 2, Timestamp: uint32(clock)},     // 1s
		{SequenceNumber: 3, Timestamp: uint32(2 * clock)}, // 2s
	}
	rep, err := timeline.ValidateRTP(frames, clock, expected, 1)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !rep.Matched {
		t.Fatalf("expected exact match, skew=%d us", rep.MaxSkewMicros)
	}
	if rep.Wraps != 0 {
		t.Fatalf("no wrap expected, got %d", rep.Wraps)
	}
}

func TestValidateRTPWrapMapsToSameBase(t *testing.T) {
	const clock = timeline.RTPClockRate
	// Start 1 second before the wrap point, then cross it twice. RTP
	// timestamps are a relative clock anchored at the first packet, so the
	// expected media times are 0s, 1s and 2s regardless of the wrap.
	wrapPoint := uint64(1) << 32
	start := uint32(wrapPoint - uint64(clock))
	ts := []uint32{
		start,
		start + clock,   // crosses the wrap
		start + 2*clock, // one second past it
	}
	frames := []timeline.RTPFrame{
		{Timestamp: ts[0]},
		{Timestamp: ts[1]},
		{Timestamp: ts[2]},
	}
	expected := []model.Micros{0, 1_000_000, 2_000_000}
	rep, err := timeline.ValidateRTP(frames, clock, expected, 1)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if rep.Wraps != 1 {
		t.Fatalf("want 1 wrap, got %d", rep.Wraps)
	}
	if !rep.Matched || rep.MaxSkewMicros > 1 {
		t.Fatalf("wrap should map exactly, skew=%d matched=%v", rep.MaxSkewMicros, rep.Matched)
	}
}

func TestValidateRTPRejectsZeroClock(t *testing.T) {
	if _, err := timeline.ValidateRTP(nil, 0, nil, 0); err == nil {
		t.Fatal("expected zero clock rate error")
	}
}

func TestValidateRTPMismatchedLength(t *testing.T) {
	frames := []timeline.RTPFrame{{Timestamp: 1}, {Timestamp: 2}}
	if _, err := timeline.ValidateRTP(frames, 90000, []model.Micros{0}, 0); err == nil {
		t.Fatal("expected length mismatch error")
	}
}

func TestParseRTPPacketsRejectsGarbage(t *testing.T) {
	if _, err := timeline.ParseRTPPackets([][]byte{{0x00}}); err == nil {
		t.Fatal("expected malformed packet error")
	}
}

func TestParseRTPPacketsRoundTrip(t *testing.T) {
	dg := timeline.EncodeRTPHeader(42, 123456, true)
	frames, err := timeline.ParseRTPPackets([][]byte{dg})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(frames) != 1 || frames[0].SequenceNumber != 42 || frames[0].Timestamp != 123456 || !frames[0].Marker {
		t.Fatalf("bad parse: %+v", frames)
	}
}

func TestPionRTPHeaderIsCompatible(t *testing.T) {
	// Confirm the fixture header decodes with pion/rtp directly, so tests
	// exercise the real dependency rather than a look-alike.
	var pkt rtp.Packet
	if err := pkt.Unmarshal(timeline.EncodeRTPHeader(7, 90000, false)); err != nil {
		t.Fatalf("pion decode: %v", err)
	}
	if pkt.SequenceNumber != 7 || pkt.Timestamp != 90000 {
		t.Fatalf("pion decoded unexpected values: %+v", pkt.Header)
	}
}
