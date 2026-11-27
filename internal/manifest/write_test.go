package manifest

import (
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/manifest/provenance"
	"continuity/internal/timeline"
)

func TestManifestBytesDeterministic(t *testing.T) {
	m := &Manifest{
		UnifiedTimescale: 48000,
		Tracks: []TrackReport{
			{Track: "audio/rtp", Kind: "audio", Timescale: 48000,
				DurationTicks: 192000, TargetBitrate: 128000, ActualBitrate: 124000,
				BitrateDeviation: "-3.13%"},
		},
		Damage: []timeline.Range{
			{Track: "audio/rtp", Reason: "packet gap", StartSeq: 10, EndSeq: 10,
				Start: 9600, End: 10560},
		},
		Quality: QualityMetrics{Basis: "structural", Computable: true, GapCount: 1},
		Provenance: Provenance{
			InputSHA256: map[string]string{"input.rtp": provenance.Sum([]byte("a"))},
			StepSHA256:  map[string]string{"scan": provenance.Sum([]byte("b"))},
		},
	}
	b1, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatal("manifest bytes not deterministic")
	}
	path := filepath.Join(t.TempDir(), "m.json")
	sum, err := m.Write(path)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if provenance.Sum(onDisk) != sum {
		t.Fatal("returned hash does not match file contents")
	}
}

func TestCombineOrderMatters(t *testing.T) {
	a := provenance.Combine("ab", "c")
	b := provenance.Combine("a", "bc")
	if a == b {
		t.Fatal("length-prefixed combine must distinguish framing")
	}
}
