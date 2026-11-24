package transport

import (
	"bytes"
	"testing"

	"archive-recovery/internal/media"
	"github.com/pion/rtp"
)

func TestRTPPreserves33BitTimestamp(t *testing.T) {
	frames := []media.FrameEvent{
		{Index: 0, Length: 100, RepairedPTS: 3000, IsFrame: true},
		// Value above 2^32 exercises the header-extension high bit.
		{Index: 1, Length: maxPayloadMTU + 50, RepairedPTS: (1 << 33) + 6000, IsFrame: true},
	}
	packets, err := Packetize(frames, 0xDEADBEEF)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) < 3 { // second frame must be fragmented
		t.Fatalf("expected fragmentation, got %d packets", len(packets))
	}
	// Only the final packet of each frame may have the marker bit.
	var markers int
	for i, raw := range packets {
		var p rtp.Packet
		if err := p.Unmarshal(raw); err != nil {
			t.Fatal(err)
		}
		if p.Marker {
			markers++
		}
		if i == 0 && !p.Marker {
			t.Fatal("single-packet frame must set marker on its only packet")
		}
	}
	if markers != 2 {
		t.Fatalf("want exactly 2 markers, got %d", markers)
	}

	got, err := Depacketize(packets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 reassembled frames, got %d", len(got))
	}
	if got[0].PTS33 != 3000 {
		t.Fatalf("frame 0 PTS wrong: %d", got[0].PTS33)
	}
	if got[1].PTS33 != (1<<33)+6000 {
		t.Fatalf("33-bit PTS lost in transport: got %d want %d",
			got[1].PTS33, (1<<33)+6000)
	}
}

func TestRTPBadPacketClassifiedRecoverable(t *testing.T) {
	if _, err := Depacketize([][]byte{{0x80, 0x00, 0x00}}); media.ClassOf(err) != media.ClassRecoverable {
		t.Fatalf("truncated RTP packet must be recoverable, got %v", err)
	}
}

func TestMP4ContainerRoundTrip(t *testing.T) {
	manifest := []byte(`{"schema_version":"manifest-1","codec":"av1","digest":"abcd"}`)
	container, err := WriteMP4Container(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// Must begin with an ftyp box per ISO BMFF layout.
	if string(container[4:8]) != "ftyp" {
		t.Fatalf("container must start with ftyp, got %q", container[4:8])
	}
	if !bytes.Contains(container, []byte("arcv")) {
		t.Fatal("arcv box type missing from container bytes")
	}
	back, err := ReadMP4Container(container)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, manifest) {
		t.Fatalf("manifest payload changed through MP4 round trip: %q", back)
	}
}

func TestMP4MissingArcvRejected(t *testing.T) {
	container, err := WriteMP4Container([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	// Truncate immediately after ftyp so the arcv box is gone.
	if _, err := ReadMP4Container(container[:32]); media.ClassOf(err) != media.ClassRecoverable {
		t.Fatalf("missing arcv must be recoverable, got %v", err)
	}
}
