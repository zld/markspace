package scan

import (
	"testing"
)

func TestParseEmsgV0(t *testing.T) {
	// v0 wire: full-box hdr(ver=0), scheme, value, timescale,
	// presentation_time_delta(u32), duration, id, message data.
	raw := cat(
		[]byte{0, 0, 0, 0},
		[]byte("urn:m\x00"), []byte("lbl\x00"),
		u32(48000),
		u32(24000), // delta => 0.5 s
		u32(4800),  // duration
		u32(11),    // id
		[]byte("payload"),
	)
	ev, ok := parseEmsgBox(raw)
	if !ok {
		t.Fatal("v0 parse failed")
	}
	if ev.TimeBase.Den != 48000 || ev.Tick != 24000 || ev.DurTick != 4800 {
		t.Fatalf("v0 fields wrong: %+v", ev)
	}
	if ev.ID != "emsg-11" || ev.Scheme != "urn:m" || string(ev.Payload) != "payload" {
		t.Fatalf("v0 id/scheme/payload wrong: %+v", ev)
	}
}

func TestParseEmsgV1Truncated(t *testing.T) {
	// Truncated before the u64 presentation time must be rejected.
	raw := cat([]byte{1, 0, 0, 0}, []byte("s\x00"), []byte("v\x00"), u32(1000), u32(1))
	if _, ok := parseEmsgBox(raw); ok {
		t.Fatal("truncated v1 emsg must not parse")
	}
	// Zero timescale is invalid.
	bad := cat([]byte{1, 0, 0, 0}, []byte("s\x00"), []byte("v\x00"), u32(0), u64(1), u32(1), u32(1))
	if _, ok := parseEmsgBox(bad); ok {
		t.Fatal("zero timescale must be rejected")
	}
	// Missing full-box header.
	if _, ok := parseEmsgBox([]byte{0, 0}); !ok {
		// expected: too short
	} else {
		t.Fatal("2-byte input must be rejected")
	}
}

func TestParseEmsgSizeGuard(t *testing.T) {
	if maxEmsgPayload != 1<<20 {
		t.Fatalf("emsg size guard changed: %d", maxEmsgPayload)
	}
}
