package ingest

import (
	"testing"

	"pwf/internal/media"
)

func TestOpusPacketDurationTOC(t *testing.T) {
	cases := []struct {
		config    int
		code      byte
		wantTicks int64 // at 48kHz
	}{
		{12, 0, 960},  // WB 20ms, 1 frame
		{12, 1, 1920}, // WB 20ms, 2 equal frames
		{12, 2, 1920}, // WB 20ms, 2 unequal frames
		{10, 0, 1920}, // WB 40ms
		{11, 0, 2880}, // WB 60ms
		{0, 0, 480},   // NB 10ms
	}
	for _, c := range cases {
		payload := OpusTestPayload(c.config, c.code)
		d, err := OpusPacketDuration(payload)
		if err != nil {
			t.Fatalf("config=%d code=%d unexpected error: %v", c.config, c.code, err)
		}
		if d.Ticks != c.wantTicks {
			t.Fatalf("config=%d code=%d got %d ticks, want %d", c.config, c.code, d.Ticks, c.wantTicks)
		}
		if d.Base.TicksPerSecond != 48000 {
			t.Fatalf("opus must use 48kHz clock, got %d", d.Base.TicksPerSecond)
		}
	}
}

func TestOpusCode3FrameCount(t *testing.T) {
	// Code 3: second byte low 6 bits encode frame count; use 4 frames.
	payload := []byte{byte(12<<3) | 0x03, 4, 1, 2, 3, 4, 5}
	d, err := OpusPacketDuration(payload)
	if err != nil {
		t.Fatal(err)
	}
	if d.Ticks != 4*960 {
		t.Fatalf("code-3 4 frames: got %d want %d", d.Ticks, 4*960)
	}

	// Invalid frame count must error, never guess.
	bad := []byte{byte(12<<3) | 0x03, 0}
	if _, err := OpusPacketDuration(bad); err == nil {
		t.Fatal("expected error for invalid code-3 frame count")
	}
}

func TestOpusEmptyPayloadErrors(t *testing.T) {
	if _, err := OpusPacketDuration(nil); err == nil {
		t.Fatal("empty Opus payload must be an error")
	}
}

func TestAACPacketDuration(t *testing.T) {
	// Two AUs per packet: each AU is one 1024-sample frame at 44.1kHz.
	payload := AACTestPayload(8, 6)
	d, err := AACPacketDuration(payload, 44100)
	if err != nil {
		t.Fatal(err)
	}
	if d.Ticks != 2*1024 {
		t.Fatalf("AAC two-AU packet: got %d ticks want %d", d.Ticks, 2*1024)
	}
}

func TestAACTruncatedPayloadRejected(t *testing.T) {
	// Header declares a 500-byte AU that the payload does not contain.
	payload := AACTestPayload(500)
	payload = payload[:6] // chop off the AU data
	if _, err := AACPacketDuration(payload, 44100); err == nil {
		t.Fatal("truncated AAC AU data must error")
	}
}

func TestAACBadAUHeaderLength(t *testing.T) {
	if _, err := AACPacketDuration([]byte{0x00, 0x08}, 44100); err == nil {
		t.Fatal("non-16-bit AU-headers-length must error")
	}
	if _, err := AACPacketDuration([]byte{0x00}, 44100); err == nil {
		t.Fatal("too-short AAC payload must error")
	}
}

func TestDurationErrorKinds(t *testing.T) {
	_, err := OpusPacketDuration(nil)
	me, ok := media.AsError(err)
	if !ok || me.Kind != media.KindUndetermined {
		t.Fatalf("expected undetermined kind, got %v", err)
	}
}
