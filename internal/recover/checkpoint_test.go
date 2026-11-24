package recover

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"testing"

	"archive/internal/model"
	"archive/internal/verify"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cp := &Checkpoint{
		InputSHA256: "abc123", InputSize: 42, Committed: 7,
		Recovered: 7, Skipped: 1, Lost: 1, MaxDevNs: 12345,
		HashState: []byte{1, 2, 3}, Done: false,
	}
	if err := Save(dir, cp); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, *cp) {
		t.Fatalf("roundtrip mismatch:\n%+v\n%+v", got, cp)
	}
}

func TestLoadMissing(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyInputRefusesDigestChange(t *testing.T) {
	cp := &Checkpoint{InputSHA256: "aaaaaaaaaaaaaaaa", InputSize: 100}
	if err := cp.VerifyInput("aaaaaaaaaaaaaaaa", 100); err != nil {
		t.Fatalf("same input refused: %v", err)
	}
	err := cp.VerifyInput("bbbbbbbbbbbbbbbb", 100)
	if err == nil {
		t.Fatal("digest change not refused")
	}
	if !contains(err.Error(), string(model.ErrCheckpointMismatch)) {
		t.Fatalf("error not tagged %s: %v", model.ErrCheckpointMismatch, err)
	}
	if err := cp.VerifyInput("aaaaaaaaaaaaaaaa", 101); err == nil {
		t.Fatal("size change not refused")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestHashStateResume proves the property the checkpoint design relies on:
// a run interrupted after "abc" and resumed with "def" produces the same
// SHA-256 as an uninterrupted run over "abcdef".
func TestHashStateResume(t *testing.T) {
	h := verify.New()
	h.Write([]byte("abc"))
	state, err := h.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	// Persist through a checkpoint, like a real interrupt would.
	dir := t.TempDir()
	cp := &Checkpoint{InputSHA256: "x", HashState: state}
	if err := Save(dir, cp); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := verify.UnmarshalState(loaded.HashState)
	if err != nil {
		t.Fatal(err)
	}
	h2.Write([]byte("def"))

	want := sha256.Sum256([]byte("abcdef"))
	if got := h2.SumHex(); got != hex.EncodeToString(want[:]) {
		t.Fatalf("resumed hash %s != uninterrupted %s", got, hex.EncodeToString(want[:]))
	}
}
