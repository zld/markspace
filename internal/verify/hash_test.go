package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestHashBytesMatchesSHA256(t *testing.T) {
	data := []byte("the quick brown fox")
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	if got := HashBytes(data); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestHashFileBounded(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1000)
	got, n, err := HashFile("mem", bytes.NewReader(data), 500)
	if err != nil {
		t.Fatal(err)
	}
	if n != 500 {
		t.Fatalf("n = %d", n)
	}
	sum := sha256.Sum256(data[:500])
	if got != hex.EncodeToString(sum[:]) {
		t.Fatal("bounded hash mismatch")
	}
}

func TestRangeHasherMatchesContiguousCopy(t *testing.T) {
	file := []byte("0123456789ABCDEFGHIJ")
	rh := NewRangeHasher(bytes.NewReader(file))
	got, n, err := rh.HashRanges([]Range{{0, 3}, {7, 2}, {19, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("n = %d", n)
	}
	want := HashBytes([]byte("01278J"))
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestRangeHasherDetectsShortRead(t *testing.T) {
	rh := NewRangeHasher(bytes.NewReader([]byte("abc")))
	if _, _, err := rh.HashRanges([]Range{{1, 10}}); err == nil {
		t.Fatal("expected an error for a short range")
	}
}

func TestCompare(t *testing.T) {
	if err := Compare("sha", "a", "a"); err != nil {
		t.Fatal(err)
	}
	if err := Compare("sha", "a", "b"); err == nil {
		t.Fatal("expected mismatch")
	}
}
