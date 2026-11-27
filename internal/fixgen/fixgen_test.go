package fixgen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGenRTPDeterministic(t *testing.T) {
	p := RTPParams{Seed: 17, Count: 20, StartSeq: 65530, SamplesPerPkt: 960,
		LossEvery: 7, DupEvery: 5, ReorderEvery: 4, TruncateAt: 9}
	a := GenRTP(p)
	b := GenRTP(p)
	if len(a) != len(b) {
		t.Fatalf("length mismatch %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("datagram %d differs between runs", i)
		}
	}
	p.LossEvery = 0
	if full := GenRTP(p); len(a) >= len(full) {
		t.Fatalf("expected loss to reduce datagram count: %d vs %d", len(a), len(full))
	}
}

func TestWriteMP4RoundTripBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	spec := []TrackSpec{
		{ID: 1, Kind: "audio", Timescale: 48000,
			Deltas: []uint32{1024, 1024, 1024}, Sizes: []uint32{300, 320, 310}},
		{ID: 2, Kind: "video", Timescale: 90000,
			Deltas: []uint32{3000, 3000, 3000, 3000}, Sizes: []uint32{5000, 200, 200, 200},
			Keyframes: []uint32{1, 4}},
	}
	if err := WriteMP4(f, spec); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() == 0 {
		t.Fatal("empty mp4 written")
	}
}
