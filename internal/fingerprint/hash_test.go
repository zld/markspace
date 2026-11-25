package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestHashIsSHA256Hex(t *testing.T) {
	h := NewHasher()
	payload := []byte("segment-payload")
	want := sha256.Sum256(payload)
	if got := h.Hash(payload); got != hex.EncodeToString(want[:]) {
		t.Fatalf("hash = %s", got)
	}
	if h.Hash(nil) != h.Hash([]byte{}) {
		t.Fatal("nil and empty payload must hash identically")
	}
}

func TestComputeMarksDuplicatesKeepsAllRecords(t *testing.T) {
	h := NewHasher()
	ids := []uint64{0, 1, 2, 3}
	payloads := [][]byte{
		[]byte("alpha"),
		[]byte("beta"),
		[]byte("alpha"), // duplicate of 0
		[]byte("alpha"), // another duplicate, still points at origin 0
	}
	tbl := h.Compute(ids, payloads)
	if len(tbl.Results) != 4 {
		t.Fatalf("all source records must be retained: %d", len(tbl.Results))
	}
	if tbl.Results[2].FirstOfHash != 0 || !tbl.Results[2].Duplicate {
		t.Fatalf("dup relationship wrong: %+v", tbl.Results[2])
	}
	if tbl.Results[3].FirstOfHash != 0 || !tbl.Results[3].Duplicate {
		t.Fatalf("second dup must reference the same origin: %+v", tbl.Results[3])
	}
	pairs := tbl.DuplicatePairs()
	if len(pairs) != 2 || pairs[0] != [2]uint64{2, 0} || pairs[1] != [2]uint64{3, 0} {
		t.Fatalf("duplicate pairs = %v", pairs)
	}
	if members := tbl.Groups[tbl.Results[0].SHA256]; len(members) != 3 {
		t.Fatalf("group members = %v", members)
	}
}

func TestComputeIsDeterministicAcrossRuns(t *testing.T) {
	h := NewHasher()
	ids := []uint64{10, 20}
	payloads := [][]byte{[]byte("x"), []byte("y")}
	first := h.Compute(ids, payloads)
	second := h.Compute(ids, payloads)
	for i := range first.Results {
		if first.Results[i] != second.Results[i] {
			t.Fatalf("re-run changed fingerprint at %d", i)
		}
	}
}
