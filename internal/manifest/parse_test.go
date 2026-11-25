package manifest

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func testManifest() Manifest {
	return Manifest{
		Seed: 17, Duration: 120 * time.Second, SegmentDuration: 6 * time.Second,
		Bitrates: []int{800000, 1600000},
		Tracks: []Track{{
			ID: 1, Kind: TrackVideo, SourceTimescale: 24000, StartTimeTicks: 180000,
			Samples: []Sample{{Index: 1, DecodeTicks: 180000, PresentationTicks: 180000, SizeBytes: 10, Sync: true}},
		}},
	}
}

func TestParseCanonicalizesRepresentations(t *testing.T) {
	data, err := json.Marshal(testManifest())
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Representations) != 2 || m.Representations[0].ID != "rep-800k" {
		t.Fatalf("bad representations: %+v", m.Representations)
	}
}

func TestParseTruncatedSampleTableIsTypedCorruption(t *testing.T) {
	m := testManifest()
	m.Tracks[0].DeclaredSampleCount = 9
	m.Fault = &FaultSpec{Kind: "truncated-stbl", TrackID: 1, Sample: 4}
	data, _ := json.Marshal(m)
	parsed, err := Parse(data)
	var corrupt *CorruptionError
	if !errors.As(err, &corrupt) {
		t.Fatalf("expected CorruptionError, got %T %v", err, err)
	}
	if corrupt.TrackID != 1 || corrupt.Sample != 4 {
		t.Fatalf("bad corruption location: %+v", corrupt)
	}
	if parsed == nil || len(parsed.Tracks[0].Samples) != 1 {
		t.Fatal("manifest must remain available for recovery report")
	}
}

func TestCanonicalBitrates(t *testing.T) {
	got, err := CanonicalBitrates([]string{"800k", "2m"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != 800000 || got[1] != 2000000 {
		t.Fatalf("bad bitrates: %v", got)
	}
	if _, err := CanonicalBitrates([]string{"fast"}); err == nil {
		t.Fatal("expected invalid bitrate error")
	}
}
