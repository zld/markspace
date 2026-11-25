package recover

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestLocalizeSingleByte(t *testing.T) {
	data := []byte("0123456789abcdef")
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])

	corrupt := append([]byte(nil), data...)
	corrupt[7] ^= 0xff
	before := append([]byte(nil), corrupt...)
	got, ok := LocalizeSingleByte(corrupt, Range{Start: 0, End: int64(len(corrupt))}, want)
	if !ok {
		t.Fatal("corrupt byte not localized")
	}
	if got != 7 {
		t.Fatalf("offset = %d, want 7", got)
	}
	if string(corrupt) != string(before) {
		t.Fatal("input was not restored to its pre-call state")
	}
}
