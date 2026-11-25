package media

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
)

func TestHashFileMatchesKnownDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.bin")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	digest, n, err := HashFile(f)
	if err != nil {
		t.Fatal(err)
	}
	// SHA-256("abc") is a fixed, well-known value.
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if digest != want {
		t.Fatalf("digest = %s", digest)
	}
	if n != 3 {
		t.Fatalf("bytes = %d", n)
	}
}

func TestHashFileStreamsLargeInput(t *testing.T) {
	// 8 MiB exercises the bounded buffer path rather than a single read.
	path := filepath.Join(t.TempDir(), "large.bin")
	data := bytes.Repeat([]byte{0xA5}, 8<<20)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, n, err := HashFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(data)) {
		t.Fatalf("bytes = %d, want %d", n, len(data))
	}
}

func TestSortFragmentsByStartIsStable(t *testing.T) {
	fs := []Fragment{
		{TrackNumber: 2, Order: 1, Start: 10},
		{TrackNumber: 1, Order: 0, Start: 10},
		{TrackNumber: 3, Order: 2, Start: 5},
	}
	SortFragmentsByStart(fs)
	if fs[0].TrackNumber != 3 {
		t.Fatalf("earliest start not first: %+v", fs)
	}
	// Equal starts keep document order.
	if fs[1].Order != 0 || fs[2].Order != 1 {
		t.Fatalf("equal-start order not stable: %+v", fs)
	}
}

func TestTrackNumbersAreSortedAndDeduplicated(t *testing.T) {
	a := &Asset{Fragments: []Fragment{
		{TrackNumber: 3}, {TrackNumber: 1}, {TrackNumber: 3}, {TrackNumber: 2},
	}}
	got := a.TrackNumbers()
	want := []uint64{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("numbers = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("numbers = %v, want %v", got, want)
		}
	}
}

func TestFragmentEndAndTrackLookup(t *testing.T) {
	a := &Asset{Tracks: []tracks.Track{{Number: 1}, {Number: 2}}}
	if a.TrackByNumber(2) == nil || a.TrackByNumber(99) != nil {
		t.Fatal("track lookup wrong")
	}
	f := Fragment{Start: timeline.Nanosecond(5), Duration: timeline.Nanosecond(7)}
	if f.End() != 12 {
		t.Fatalf("end = %d", f.End())
	}
}
