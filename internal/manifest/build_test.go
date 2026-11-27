package manifest

import (
	"path/filepath"
	"testing"

	"continuity/internal/media"
	"continuity/internal/rtp"
)

func sample() *Manifest {
	return &Manifest{
		Seed: 17,
		Tracks: []media.TrackParams{
			{Number: 1, Kind: "video", Source: "matroska", ColorSpace: "bt709"},
			{Number: 2, Kind: "audio", Source: "matroska", Channels: 2, SampleRate: 48000},
		},
		Decisions: []media.Decision{
			{Track: 3, Aspect: "sample-rate", From: "44100", To: "48000", Reason: "test"},
		},
		RTP: RTPStats{
			Received: 24, Lost: 3, Duplicated: 2, Late: 1,
			Missing: []rtp.Range{{From: 65540, To: 65542}},
		},
		Recovery: []Recovery{{Seq: 65543, TimestampNs: 1_880_000_000}},
		Sync:     SyncSummary{Pairs: 41, MaxDeviationNs: 15_000_000},
		Events:   142,
	}
}

func TestBytesDeterministic(t *testing.T) {
	m := sample()
	a, b := m.Bytes(), m.Bytes()
	if string(a) != string(b) {
		t.Fatal("serialization not deterministic")
	}
	if SHA256(a) != SHA256(b) {
		t.Fatal("hash not deterministic")
	}
}

func TestHashSensitive(t *testing.T) {
	m := sample()
	h1 := SHA256(m.Bytes())
	m.Events = 143
	if SHA256(m.Bytes()) == h1 {
		t.Fatal("hash insensitive to content")
	}
}

func TestWriteRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.json")
	m := sample()
	sha, err := Write(path, m)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if sha != SHA256(m.Bytes()) {
		t.Fatal("returned hash differs from content hash")
	}
}
