package generator

import (
	"bytes"
	"testing"
)

func TestGenerateIsDeterministic(t *testing.T) {
	opts := Options{Seed: 7, SegmentCount: 4, SegmentSize: 128}
	a, err := Generate(opts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("length mismatch: %d != %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i].Data, b[i].Data) {
			t.Fatalf("segment %d differs between runs", i)
		}
		if a[i].Index != i {
			t.Fatalf("segment %d has index %d", i, a[i].Index)
		}
		if !bytes.Equal(a[i].Data, segmentBytes(opts.Seed, i, len(a[i].Data))) {
			t.Fatalf("segment %d does not match its own seed derivation", i)
		}
	}
}

func TestDifferentSeedsDifferentBytes(t *testing.T) {
	a, err := Generate(Options{Seed: 1, SegmentCount: 1, SegmentSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(Options{Seed: 2, SegmentCount: 1, SegmentSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a[0].Data, b[0].Data) {
		t.Fatal("different seeds produced identical content")
	}
}

func TestSegmentsAreDistinct(t *testing.T) {
	segs, err := Generate(Options{Seed: 3, SegmentCount: 5, SegmentSize: 64})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			if bytes.Equal(segs[i].Data, segs[j].Data) {
				t.Fatalf("segments %d and %d are identical", i, j)
			}
		}
	}
}

func TestValidateLimits(t *testing.T) {
	if err := (Options{SegmentCount: 0, SegmentSize: 1}).Validate(); err == nil {
		t.Fatal("zero segments accepted")
	}
	if err := (Options{SegmentCount: MaxSegments + 1, SegmentSize: 1}).Validate(); err == nil {
		t.Fatal("over-limit segment count accepted")
	}
	if err := (Options{SegmentCount: MaxSegments, SegmentSize: 1}).Validate(); err != nil {
		t.Fatalf("limit itself rejected: %v", err)
	}
	if err := (Options{SegmentCount: 1, SegmentSize: 0}).Validate(); err == nil {
		t.Fatal("zero segment size accepted")
	}
}

func TestCorruptDoesNotMutateOriginal(t *testing.T) {
	orig := []byte("abcdefghij")
	got := Corrupt(orig)
	if bytes.Equal(orig, got) {
		t.Fatal("corruption did not change the copy")
	}
	if string(orig) != "abcdefghij" {
		t.Fatalf("original was mutated: %q", orig)
	}
	diff := 0
	for i := range orig {
		if orig[i] != got[i] {
			diff++
		}
	}
	if diff != 1 {
		t.Fatalf("%d bytes differ, want 1", diff)
	}
	if out := Corrupt(nil); len(out) == 0 {
		t.Fatal("corrupting empty input produced no bytes")
	}
}

func TestDropKeepsOriginalIndices(t *testing.T) {
	segs, err := Generate(Options{Seed: 3, SegmentCount: 5, SegmentSize: 8})
	if err != nil {
		t.Fatal(err)
	}
	kept := Drop(segs, 2)
	if len(kept) != 4 {
		t.Fatalf("kept %d segments, want 4", len(kept))
	}
	for _, s := range kept {
		if s.Index == 2 {
			t.Fatal("dropped segment still present")
		}
	}
	if len(segs) != 5 {
		t.Fatal("Drop mutated its input")
	}
}
