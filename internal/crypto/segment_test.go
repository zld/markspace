package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"testing"
)

func testAEAD(t *testing.T) cipher.AEAD {
	t.Helper()
	key := bytes.Repeat([]byte{0x42}, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return aead
}

func TestSealOpenRoundTrip(t *testing.T) {
	aead := testAEAD(t)
	nonce := bytes.Repeat([]byte{1}, NonceSize)
	plain := []byte("the quick brown fox")

	sealed, err := Seal(aead, nonce, plain, "asset-1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) != len(plain)+TagSize {
		t.Fatalf("sealed length = %d, want %d", len(sealed), len(plain)+TagSize)
	}
	got, err := Open(aead, nonce, sealed, "asset-1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip mismatch: %q != %q", got, plain)
	}
}

func TestSealIsNondeterministicWithDistinctNonces(t *testing.T) {
	aead := testAEAD(t)
	plain := []byte("payload")
	a, err := Seal(aead, bytes.Repeat([]byte{1}, NonceSize), plain, "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Seal(aead, bytes.Repeat([]byte{2}, NonceSize), plain, "a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("different nonces produced identical ciphertext")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	aead := testAEAD(t)
	nonce := bytes.Repeat([]byte{7}, NonceSize)
	sealed, err := Seal(aead, nonce, []byte("hello"), "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	sealed[0] ^= 0xff
	if _, err := Open(aead, nonce, sealed, "asset-1", 0); !errors.Is(err, ErrAuth) {
		t.Fatalf("tampered ciphertext: %v, want ErrAuth", err)
	}
}

func TestAADBindsAssetAndIndex(t *testing.T) {
	aead := testAEAD(t)
	nonce := bytes.Repeat([]byte{7}, NonceSize)
	sealed, err := Seal(aead, nonce, []byte("hello"), "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(aead, nonce, sealed, "asset-2", 0); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong asset: %v, want ErrAuth", err)
	}
	if _, err := Open(aead, nonce, sealed, "asset-1", 1); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong index: %v, want ErrAuth", err)
	}
}

func TestWrongNonceAndShortInput(t *testing.T) {
	aead := testAEAD(t)
	nonce := bytes.Repeat([]byte{7}, NonceSize)
	sealed, err := Seal(aead, nonce, []byte("hello"), "asset-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(aead, bytes.Repeat([]byte{8}, NonceSize), sealed, "asset-1", 0); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong nonce: %v, want ErrAuth", err)
	}
	if _, err := Open(aead, nonce, sealed[:TagSize-1], "asset-1", 0); !errors.Is(err, ErrShortCipher) {
		t.Fatalf("short ciphertext: %v, want ErrShortCipher", err)
	}
	if _, err := Open(aead, nonce[:5], sealed, "asset-1", 0); !errors.Is(err, ErrBadNonce) {
		t.Fatalf("short nonce: %v, want ErrBadNonce", err)
	}
}

func TestNewNonceRejectsBadSource(t *testing.T) {
	if _, err := NewNonce(nil, NonceSize); err == nil {
		t.Fatal("nil source accepted")
	}
	if _, err := NewNonce(bytes.NewReader(nil), 0); err == nil {
		t.Fatal("zero size accepted")
	}
	if _, err := NewNonce(bytes.NewReader([]byte{1, 2}), NonceSize); err == nil {
		t.Fatal("short source accepted")
	}
}
