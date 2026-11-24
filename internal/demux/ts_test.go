package demux

import (
	"testing"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
)

func TestParseTSStreamsAndEvents(t *testing.T) {
	p := fixture.BuildTS()
	media, err := ParseTS(p.Data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(media.Tracks) != 2 {
		t.Fatalf("tracks: %d", len(media.Tracks))
	}
	if media.Tracks[0].Kind != "video" || media.Tracks[1].Kind != "audio" {
		t.Fatalf("kinds: %+v", media.Tracks)
	}
	if len(media.Events) != 1 || media.Events[0].Kind != "sei" {
		t.Fatalf("events: %+v", media.Events)
	}
}

func TestTSContinuityErrorOffset(t *testing.T) {
	p := fixture.BuildTS()
	// Corrupt the continuity counter of the second packet for the same PID.
	// Each PID is written once, so pack the packet into a fresh stream first.
	data := append([]byte(nil), p.Data...)
	// Repeating packet 3 (video) after itself with a broken counter makes the
	// discontinuity visible on that PID.
	dup := append([]byte(nil), p.Data[376:564]...)
	dup[3] = (dup[3] & 0xf0) | 0x07
	data = append(append(data[:564:564], dup...), p.Data[564:]...)
	_, err := ParseTS(data, Options{})
	fe, ok := fault.As(err)
	if !ok || fe.Operation != fault.OpSequence {
		t.Fatalf("expected continuity fault, got %v", err)
	}
	if fe.InputOffset != 564 {
		t.Fatalf("offset: %d want 564", fe.InputOffset)
	}
}

func TestTSTruncatedPacketOffset(t *testing.T) {
	p := fixture.BuildTS()
	data := p.Data[:len(p.Data)-50]
	_, err := ParseTS(data, Options{})
	fe, ok := fault.As(err)
	if !ok || fe.Operation != fault.OpTruncated {
		t.Fatalf("expected truncation fault, got %v", err)
	}
	if fe.InputOffset != int64(len(data)) {
		t.Fatalf("offset: %d want %d", fe.InputOffset, len(data))
	}
}
