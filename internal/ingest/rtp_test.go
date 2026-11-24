package ingest

import (
	"path/filepath"
	"testing"

	"mediarc/internal/model"
)

func TestRTPRoundTripPreservesOffsetsAndInjections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.mrtp")
	fx, err := GenerateRTPFile(RTPGenOptions{
		Path: path, Seed: 17, Packets: 40, Late: true, Duplicate: true, OutOfOrder: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fx.StoredRecords != 41 {
		t.Fatalf("records=%d want 41", fx.StoredRecords)
	}
	packets, err := ReadRTPFile(path, &model.Collector{})
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 41 {
		t.Fatalf("read=%d want 41", len(packets))
	}
	for i, p := range packets {
		if p.Offset != fx.Offsets[i] {
			t.Fatalf("offset %d mismatch: %d != %d", i, p.Offset, fx.Offsets[i])
		}
		if p.Packet.Header.SSRC == 0 || len(p.Packet.Payload) == 0 {
			t.Fatalf("packet %d was not parsed by pion/rtp", i)
		}
	}
}
