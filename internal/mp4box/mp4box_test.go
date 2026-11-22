package mp4box

import (
	"testing"
)

func TestBuildParseRoundTrip(t *testing.T) {
	tab := SampleTable{
		TrackID:         1,
		Timescale:       24000,
		SampleSizes:     []uint32{120, 240, 120, 360},
		SampleDeltas:    []uint32{1001, 1001, 1001, 1001},
		ChunkOffsets:    []uint32{1024},
		SamplesPerChunk: 4,
		Payload:         []byte("synthetic-frame-payload"),
	}
	col := &Color{Primaries: 1, Transfer: 1, Matrix: 1, FullRange: true}
	parsed, raw, err := VerifyRoundTrip(tab, col)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Color == nil || !parsed.Color.FullRange || parsed.Color.Primaries != 1 {
		t.Fatalf("colr round-trip = %+v", parsed.Color)
	}
	// Deterministic serialization.
	raw2, err := BuildFile(tab, col)
	if err != nil || string(raw) != string(raw2) {
		t.Fatalf("non-deterministic MP4 bytes: err=%v", err)
	}
	if len(raw) < 8 || string(raw[4:8]) != "ftyp" {
		t.Fatal("output does not start with an ftyp box")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := ParseFile([]byte{0, 1, 2, 3, 4, 5, 6, 7}); err == nil {
		t.Fatal("garbage input must not parse")
	}
	if _, err := BuildFile(SampleTable{Timescale: 0}, nil); err == nil {
		t.Fatal("zero timescale must be rejected")
	}
}

func TestNoColorBoxRoundTrip(t *testing.T) {
	tab := SampleTable{TrackID: 3, Timescale: 48000, SampleSizes: []uint32{8},
		SampleDeltas: []uint32{1024}, ChunkOffsets: []uint32{512}, SamplesPerChunk: 1}
	parsed, _, err := VerifyRoundTrip(tab, nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Color != nil {
		t.Fatalf("unexpected colr: %+v", parsed.Color)
	}
}
