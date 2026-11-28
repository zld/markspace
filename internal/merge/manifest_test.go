package merge_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"rtptrace/internal/merge"
	"rtptrace/internal/stage"
)

func sampleRows() []merge.PacketRow {
	// Deliberately unordered across tracks and sequences.
	return []merge.PacketRow{
		{TrackIndex: 1, SSRC: 0x4001, Sequence: 2, RTPTimestamp: 1800, MediaDTS: 960,
			Offset: 100, Size: 4, Payload: []byte("p1-2")},
		{TrackIndex: 0, SSRC: 0x4000, Sequence: 1, RTPTimestamp: 900, MediaDTS: 480,
			Offset: 40, Size: 4, Payload: []byte("p0-1")},
		{TrackIndex: 0, SSRC: 0x4000, Sequence: 0, RTPTimestamp: 0, MediaDTS: 0,
			Offset: 20, Size: 4, Payload: []byte("p0-0")},
		{TrackIndex: 1, SSRC: 0x4001, Sequence: 0, RTPTimestamp: 0, MediaDTS: 0,
			Offset: 80, Size: 4, Payload: []byte("p1-0")},
	}
}

func TestBuildSortsByTrackThenSequence(t *testing.T) {
	m, _, err := merge.Build("digest", 17, sampleRows(), merge.Stats{EmittedPackets: 4})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(m.Entries) != 4 {
		t.Fatalf("entries=%d want 4", len(m.Entries))
	}
	want := []struct {
		track int
		seq   uint16
	}{
		{0, 0}, {0, 1}, {1, 0}, {1, 2},
	}
	for i, w := range want {
		got := m.Entries[i]
		if got.TrackIndex != w.track || got.Sequence != w.seq {
			t.Fatalf("entry %d got (t=%d s=%d) want (t=%d s=%d)",
				i, got.TrackIndex, got.Sequence, w.track, w.seq)
		}
	}
}

func TestStableKeyComponents(t *testing.T) {
	k1 := merge.StableKey("digest", 0, stage.StageVersion, 5)
	k2 := merge.StableKey("digest", 0, stage.StageVersion, 5)
	k3 := merge.StableKey("digest", 1, stage.StageVersion, 5)
	k4 := merge.StableKey("other", 0, stage.StageVersion, 5)
	if k1 != k2 {
		t.Fatal("same inputs must yield identical key")
	}
	if k1 == k3 || k1 == k4 {
		t.Fatal("track and input digest must participate in the key")
	}
	if len(k1) != 64 {
		t.Fatalf("key should be hex sha256, len=%d", len(k1))
	}
}

func TestByteIdenticalAcrossRuns(t *testing.T) {
	stats := merge.Stats{Received: 4, EmittedPackets: 4, DuplicatePackets: 1, LostPackets: 2}
	m1, a1, err := merge.Build("digest", 17, sampleRows(), stats)
	if err != nil {
		t.Fatalf("build 1: %v", err)
	}
	m2, a2, err := merge.Build("digest", 17, sampleRows(), stats)
	if err != nil {
		t.Fatalf("build 2: %v", err)
	}
	j1, _ := merge.EncodeManifest(m1)
	j2, _ := merge.EncodeManifest(m2)
	if string(j1) != string(j2) {
		t.Fatal("manifest bytes differ across identical runs")
	}
	if string(a1) != string(a2) {
		t.Fatal("artifact bytes differ across identical runs")
	}
	// Artifact hash in manifest matches the actual artifact.
	sum := sha256.Sum256(a1)
	if m1.ArtifactSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("manifest artifact hash does not match artifact bytes")
	}
}

func TestPayloadHashesEmbedded(t *testing.T) {
	m, artifact, err := merge.Build("d", 1, sampleRows(), merge.Stats{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Artifact layout: 32-byte hash then payload per row.
	first := m.Entries[0]
	sum := sha256.Sum256([]byte("p0-0"))
	if first.PayloadHash != hex.EncodeToString(sum[:]) {
		t.Fatal("payload hash mismatch")
	}
	if string(artifact[32:36]) != "p0-0" {
		t.Fatalf("artifact payload layout wrong: %q", artifact[32:36])
	}
}
