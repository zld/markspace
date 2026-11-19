package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"

	"mediakey/internal/manifest"
)

func sampleKey() []byte {
	k := make([]byte, KeySize)
	_, _ = rand.Read(k)
	return k
}

// decryptErr discards plaintext and returns only the error for assertions.
func decryptErr(key []byte, env *manifest.Envelope) error {
	_, err := Decrypt(key, env)
	return err
}

func TestRoundTripAndTamper(t *testing.T) {
	key := sampleKey()
	plain := bytes.Repeat([]byte("MK"), 200)
	const keyID = "key-r001"
	env, err := Encrypt(rand.Reader, key, "asset-1", 4, keyID, plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(key, env)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip failed: %v", err)
	}
	// Ciphertext tamper -> auth_failed.
	tampered := *env
	tampered.Ciphertext = append([]byte(nil), env.Ciphertext...)
	tampered.Ciphertext[0] ^= 0x01
	if code := manifest.ErrorCode(decryptErr(key, &tampered)); code != manifest.ReasonAuthFailed {
		t.Fatalf("tamper code = %s, want auth_failed", code)
	}
	// Metadata swap (wrong chunk index changes the AAD) -> auth_failed.
	swapped := *env
	swapped.ChunkIndex = 5
	if code := manifest.ErrorCode(decryptErr(key, &swapped)); code != manifest.ReasonAuthFailed {
		t.Fatalf("AAD swap code = %s, want auth_failed", code)
	}
	// Truncated tag -> truncated category.
	short := *env
	short.Ciphertext = env.Ciphertext[:4]
	if code := manifest.ErrorCode(decryptErr(key, &short)); code != manifest.ReasonTruncated {
		t.Fatalf("truncation code = %s, want truncated", code)
	}
	// Wrong key -> auth_failed.
	if code := manifest.ErrorCode(decryptErr(sampleKey(), env)); code != manifest.ReasonAuthFailed {
		t.Fatalf("wrong-key code = %s, want auth_failed", code)
	}
}

func TestEncryptValidation(t *testing.T) {
	plain := []byte("hello")
	if _, err := Encrypt(rand.Reader, []byte("short"), "a", 0, "k", plain); err == nil {
		t.Fatal("short key must be rejected")
	}
	if _, err := Encrypt(rand.Reader, sampleKey(), "", 0, "k", plain); err == nil {
		t.Fatal("missing asset id must be rejected")
	}
}
