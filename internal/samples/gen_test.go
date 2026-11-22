package samples

import (
	"bytes"
	"testing"

	"mediaqc/internal/ebml"
	"mediaqc/internal/timeline"
)

func scanSpec(t *testing.T, s Spec) (*ebml.ScanResult, error) {
	t.Helper()
	data := Build(s)
	return ebml.Scan(bytes.NewReader(data), int64(len(data)))
}

func TestNormalScans(t *testing.T) {
	res, err := scanSpec(t, Normal())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(res.Tracks))
	}
	if len(res.Blocks) != 36 {
		t.Fatalf("blocks = %d, want 36", len(res.Blocks))
	}
	keys := 0
	for _, b := range res.Blocks {
		if b.Keyframe {
			keys++
		}
	}
	if keys != 3 {
		t.Fatalf("keyframes = %d, want 3", keys)
	}
}

func TestWrapAndDisorderScan(t *testing.T) {
	for name, s := range map[string]Spec{"wrap": Wrap(), "disorder": Disorder()} {
		res, err := scanSpec(t, s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(res.Blocks) != 36 {
			t.Fatalf("%s: blocks = %d", name, len(res.Blocks))
		}
	}
	// Wrap sample must contain a >5s backward jump in raw timecodes.
	res, _ := scanSpec(t, Wrap())
	var prev int64
	found := false
	for _, b := range res.Blocks {
		if b.Track == 1 {
			if prev-b.RawTimecode > 5000 {
				found = true
			}
			prev = b.RawTimecode
		}
	}
	if !found {
		t.Fatalf("wrap sample has no large backward jump")
	}
}

func TestTruncationsFault(t *testing.T) {
	for name, s := range map[string]Spec{
		"trunc-unknown": TruncUnknown(),
		"trunc-cluster": TruncCluster(),
	} {
		_, err := scanSpec(t, s)
		f := timeline.AsFault(err)
		if f == nil || f.Class != timeline.ClassTruncated {
			t.Fatalf("%s: want truncated fault, got %v", name, err)
		}
		if f.Offset <= 0 {
			t.Fatalf("%s: fault offset %d not positioned", name, f.Offset)
		}
	}
}

func TestWriteAllDeterministic(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	if err := WriteAll(d1); err != nil {
		t.Fatal(err)
	}
	if err := WriteAll(d2); err != nil {
		t.Fatal(err)
	}
	for _, name := range Names() {
		a, err := readFile(d1, name)
		if err != nil {
			t.Fatal(err)
		}
		b, err := readFile(d2, name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%s not deterministic", name)
		}
	}
}
