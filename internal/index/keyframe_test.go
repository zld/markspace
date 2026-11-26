package index

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"liveregress/internal/fault"
)

func facts() []Keyframe {
	return []Keyframe{
		{Time: 0, Offset: 100, Size: 50, TrackID: 1},
		{Time: 90000, Offset: 900, Size: 40, TrackID: 1},
		{Time: 180000, Offset: 1700, Size: 60, TrackID: 1},
	}
}

func TestBuildAndVerifyPerfect(t *testing.T) {
	idx := BuildFromFacts("v0", 90000, "fmp4", facts())
	hits, fp, missed := Verify(idx, facts())
	if hits != 3 || fp != 0 || missed != 0 {
		t.Fatalf("hits=%d fp=%d missed=%d", hits, fp, missed)
	}
	if err := CheckHits(idx, facts()); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDetectsFalsePositive(t *testing.T) {
	idx := BuildFromFacts("v0", 90000, "fmp4", facts())
	// Move one entry to an offset that is not a keyframe.
	idx.Entries[1].Offset = 1200
	hits, fp, _ := Verify(idx, facts())
	if hits != 2 || fp != 1 {
		t.Fatalf("hits=%d fp=%d", hits, fp)
	}
	if err := CheckHits(idx, facts()); !errors.Is(err, fault.ErrIndex) {
		t.Fatalf("want index fault, got %v", err)
	}
}

func TestVerifyDetectsMissed(t *testing.T) {
	idx := BuildFromFacts("v0", 90000, "fmp4", facts()[:2])
	_, _, missed := Verify(idx, facts())
	if missed != 1 {
		t.Fatalf("missed=%d", missed)
	}
}

func TestSaveLoadRoundTripStable(t *testing.T) {
	idx := BuildFromFacts("v0", 90000, "rebuilt", facts())
	idx.InputHash = "abc123"
	path := filepath.Join(t.TempDir(), "v0.idx.json")
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Digest != idx.Digest || loaded.Digest == "" {
		t.Fatalf("digest mismatch: %q vs %q", loaded.Digest, idx.Digest)
	}
	if err := loaded.Save(path); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("re-saving changed bytes")
	}
	// A different input digest must change the index digest.
	other := BuildFromFacts("v0", 90000, "rebuilt", facts())
	other.InputHash = "def456"
	if other.Digest0() == idx.Digest0() {
		t.Fatal("input hash should affect digest")
	}
}

func TestLoadRejectsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	os.WriteFile(path, []byte(`{"layer":"v0","timescale":90000,"entries":[]}`), 0o644)
	_, err := Load(path)
	if !errors.Is(err, fault.ErrIndex) {
		t.Fatalf("want index fault, got %v", err)
	}
}
