package sample

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	pionrtp "github.com/pion/rtp"

	"mediarc/model"
	rtpkernel "mediarc/rtp"
)

// readRTPFile decodes the length-prefixed packet file.
func readRTPFile(t *testing.T, path string) []*pionrtp.Packet {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var packets []*pionrtp.Packet
	for offset := 0; offset < len(data); {
		if offset+4 > len(data) {
			t.Fatalf("truncated length prefix at %d", offset)
		}
		length := int(binary.BigEndian.Uint32(data[offset:]))
		offset += 4
		if offset+length > len(data) {
			t.Fatalf("truncated packet at %d", offset)
		}
		packet := &pionrtp.Packet{}
		if err := packet.Unmarshal(data[offset : offset+length]); err != nil {
			t.Fatalf("unmarshal packet at %d: %v", offset, err)
		}
		offset += length
		packets = append(packets, packet)
	}
	return packets
}

func TestRTPCleanFixture(t *testing.T) {
	dir := t.TempDir()
	stream, err := BuildRTP(dir, "clean.rtp", RTPOptions{Packets: 30, TimestampJumpAt: -1})
	if err != nil {
		t.Fatal(err)
	}
	window, err := rtpkernel.NewWindow(rtpkernel.Config{Depth: 4, PacketDurationNS: 20_000_000, ClockRate: 90000, TimestampJumps: true})
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := rtpkernel.NewRecovery(rtpkernel.RecoveryConfig{Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	var segments []rtpkernel.Segment
	for _, packet := range readRTPFile(t, filepath.Join(dir, stream.Name)) {
		release, err := window.Push(rtpkernel.Arrival{Packet: packet})
		if err != nil {
			t.Fatalf("push seq %d: %v", packet.SequenceNumber, err)
		}
		segments = append(segments, recovery.Apply(release)...)
	}
	flush := window.Flush()
	segments = append(segments, recovery.Apply(flush)...)
	if stream.Lost != 0 || stream.ExpectedGaps != 0 {
		t.Fatalf("clean fixture declares loss: %+v", stream)
	}
	if len(segments) != 30 {
		t.Fatalf("segments = %d, want 30", len(segments))
	}
	for _, segment := range segments {
		if segment.Kind != rtpkernel.KindFrame {
			t.Fatalf("clean stream produced %s", segment.Kind)
		}
	}
}

func TestRTPLossFixtureMatchesGaps(t *testing.T) {
	dir := t.TempDir()
	stream, err := BuildRTP(dir, "lossy.rtp", RTPOptions{
		Packets: 40, LossEvery: 7, ReorderEvery: 5, DuplicateEvery: 11, TimestampJumpAt: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	window, err := rtpkernel.NewWindow(rtpkernel.Config{Depth: 8, PacketDurationNS: 20_000_000, ClockRate: 90000})
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := rtpkernel.NewRecovery(rtpkernel.RecoveryConfig{Silent: true})
	if err != nil {
		t.Fatal(err)
	}
	var silence int
	for _, packet := range readRTPFile(t, filepath.Join(dir, stream.Name)) {
		release, err := window.Push(rtpkernel.Arrival{Packet: packet})
		if err != nil && !mediarcIsRecoverable(err) {
			t.Fatalf("push seq %d: %v", packet.SequenceNumber, err)
		}
		for _, segment := range recovery.Apply(release) {
			if segment.Kind == rtpkernel.KindSilence {
				silence++
			}
		}
	}
	for _, segment := range recovery.Apply(window.Flush()) {
		if segment.Kind == rtpkernel.KindSilence {
			silence++
		}
	}
	stats := recovery.Stats()
	if stats.Gaps != stream.ExpectedGaps {
		t.Fatalf("gaps = %d, want %d", stats.Gaps, stream.ExpectedGaps)
	}
	if silence != stream.ExpectedGaps {
		t.Fatalf("silence segments = %d, want %d", silence, stream.ExpectedGaps)
	}
	if stats.Recovered != stream.ExpectedGaps {
		t.Fatalf("recovered = %d, want %d", stats.Recovered, stream.ExpectedGaps)
	}
}

func mediarcIsRecoverable(err error) bool {
	return model.IsCode(err, model.CodeDuplicatePacket) || model.IsCode(err, model.CodeLatePacket)
}
