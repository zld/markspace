package model

import (
	"testing"

	"example.com/mediavault/internal/clock"
)

func TestWAVFormatValidation(t *testing.T) {
	good := WAVFormat{SampleRate: clock.NewRate(48000, 1), Channels: 2, BitsPerSample: 24}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid format rejected: %v", err)
	}
	if good.FrameBytes() != 6 {
		t.Fatalf("frame bytes = %d", good.FrameBytes())
	}
	cases := []WAVFormat{
		{SampleRate: clock.NewRate(48000, 1), Channels: 0, BitsPerSample: 16},
		{SampleRate: clock.NewRate(48000, 1), Channels: 9, BitsPerSample: 16},
		{SampleRate: clock.NewRate(48000, 1), Channels: 2, BitsPerSample: 8},
		{SampleRate: clock.NewRate(4000, 1), Channels: 2, BitsPerSample: 16},
		{SampleRate: clock.NewRate(400000, 1), Channels: 2, BitsPerSample: 16},
		{SampleRate: clock.Rate{}, Channels: 2, BitsPerSample: 16},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d accepted: %+v", i, c)
		}
	}
}

func TestAssetKeyIgnoresPathAndName(t *testing.T) {
	digest := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	a := AssetKey(TransportWAV, digest)
	b := AssetKey(TransportWAV, digest)
	if a != b {
		t.Fatal("identical content must produce identical keys")
	}
	if AssetKey(TransportRTP, digest) == a {
		t.Fatal("transports must not collide")
	}
	if AssetKey(TransportWAV, digest) == AssetKey(TransportWAV, "x") {
		t.Fatal("different content must not collide")
	}
}

func TestSortedChunksDoesNotMutate(t *testing.T) {
	a := Asset{Chunks: []ChunkRef{{Index: 2}, {Index: 0}, {Index: 1}}}
	sorted := a.SortedChunks()
	for i, c := range sorted {
		if c.Index != i {
			t.Fatalf("chunk %d has index %d", i, c.Index)
		}
	}
	if a.Chunks[0].Index != 2 {
		t.Fatal("SortedChunks mutated the asset")
	}
	if a.TotalLength() != 0 {
		t.Fatal("total length should sum stored lengths")
	}
	a.Chunks = []ChunkRef{{Length: 10}, {Length: 5}}
	if a.TotalLength() != 15 {
		t.Fatalf("total length = %d", a.TotalLength())
	}
}

func TestStageOrdering(t *testing.T) {
	stages := []Stage{StageReceived, StageScanned, StageFingerprinted, StageNormalized, StageArchived}
	for i := 1; i < len(stages); i++ {
		if stages[i-1].Order() >= stages[i].Order() {
			t.Fatalf("%s not before %s", stages[i-1], stages[i])
		}
	}
}
