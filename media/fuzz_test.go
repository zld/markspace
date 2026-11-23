package media

import (
	"bytes"
	"testing"
)

// FuzzProbeFile ensures the parser never panics and never accepts an input that
// violates its own invariants, no matter how the bytes are mutated.
func FuzzProbeFile(f *testing.F) {
	fx, err := BuildFixture(FixtureOptions{
		Seed: 41,
		Tracks: []FixtureTrack{
			{ID: 1, Codec: "avc1", Lang: "eng", Timescale: 90000, Samples: 8, SyncEvery: 4},
			{ID: 2, Codec: "mp4a", Lang: "eng", Timescale: 48000, Samples: 8},
		},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(fx.Bytes)
	f.Add(fx.Bytes[:len(fx.Bytes)/2])
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ProbeFile(bytes.NewReader(data), int64(len(data)), "fuzz")
		if err != nil {
			return
		}
		if p == nil {
			t.Fatal("nil probe with nil error")
		}
		// Any accepted file must satisfy the bounds invariant or be rejected
		// here; both outcomes are fine, but a silent bad sample is not.
		if err := ValidateMovieBounds(p); err != nil {
			return
		}
		for _, ti := range p.Tracks {
			if ti.Table == nil {
				t.Fatalf("track %d has no table", ti.TrackID)
			}
			if uint32(len(ti.Table.Samples)) != ti.SampleCount {
				t.Fatalf("track %d row count mismatch", ti.TrackID)
			}
			for i := range ti.Table.Samples {
				s := ti.Table.Samples[i]
				if s.Offset+uint64(s.Size) < s.Offset {
					t.Fatalf("sample %d offset overflows", s.Index)
				}
			}
		}
	})
}
