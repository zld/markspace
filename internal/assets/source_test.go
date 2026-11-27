package assets_test

import (
	"testing"

	"media-check/internal/assets"
)

func TestSealIsContentAddressedAndImmutable(t *testing.T) {
	data := []byte("rtp-capture-seed-17")
	src := assets.Seal("capture", data)
	if src.ID != assets.Digest(data) {
		t.Fatal("sealed id must equal payload digest")
	}
	if err := src.Verify(); err != nil {
		t.Fatalf("fresh source failed verify: %v", err)
	}
	// Mutating the caller's slice must not change the sealed asset.
	data[0] = 'X'
	if err := src.Verify(); err != nil {
		t.Fatalf("sealed payload must be a defensive copy: %v", err)
	}
	got := src.Bytes()
	got[0] = 'Z'
	if err := src.Verify(); err != nil {
		t.Fatalf("Bytes() must return a copy: %v", err)
	}
}

func TestDifferentBytesDifferentIdentity(t *testing.T) {
	a := assets.Seal("a", []byte("one"))
	b := assets.Seal("b", []byte("two"))
	if a.ID == b.ID {
		t.Fatal("distinct content must produce distinct identities")
	}
}
