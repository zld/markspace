package essence

import (
	"testing"

	"github.com/pion/rtp"
)

func TestRPTSampleRoundTrip(t *testing.T) {
	h := Header{StartTick: 45000, TrackID: 9, Flags: FlagSync | FlagRTP}
	rh := RPTSample(7, 45000, 9, false)
	body, err := rh.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	frame := Build(h, append(body, Payload(1, 20)...))
	parsed, err := ParseHeader(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.HasRTP() {
		t.Fatal("RTP flag lost")
	}
	rt, err := ParseRTP(parsed, frame)
	if err != nil {
		t.Fatal(err)
	}
	if rt.SequenceNumber != 7 || rt.SSRC != 9 {
		t.Fatalf("header = %+v", rt)
	}
	if got := RTPTimestampTicks(parsed, rt); got != 45000 {
		t.Fatalf("ticks = %d", got)
	}
}

func TestRTPTimestampUnwrap(t *testing.T) {
	// A start tick past the 32-bit boundary unwraps to the nearest congruent
	// value rather than the raw low 32 bits.
	start := uint64(1)<<32 + 100
	h := Header{StartTick: start}
	rh := RPTSample(1, start, 1, false)
	rt := &rtp.Header{Timestamp: rh.Timestamp}
	if got := RTPTimestampTicks(h, rt); got != start {
		t.Fatalf("ticks = %d, want %d", got, start)
	}
}

func TestParseRTPRejectsMissingFlag(t *testing.T) {
	h := Header{StartTick: 0}
	if _, err := ParseRTP(h, []byte{}); err == nil {
		t.Fatal("expected error for frame without RTP flag")
	}
}

func TestParseRTPRejectsShortPayload(t *testing.T) {
	h := Header{Flags: FlagRTP}
	if _, err := ParseRTP(h, make([]byte, 10)); err == nil {
		t.Fatal("expected error for truncated header")
	}
}
