package asset

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func validSegment(i int) Segment {
	nonce := make([]byte, NonceSize)
	for k := range nonce {
		nonce[k] = byte(i)
	}
	return Segment{
		Index:        i,
		KeyID:        "k1",
		KeyVersion:   1,
		Nonce:        EncodeNonce(nonce),
		PlainDigest:  strings.Repeat("ab", 32),
		CipherDigest: strings.Repeat("cd", 32),
		PlainSize:    16,
		CipherSize:   32,
	}
}

func TestEncodeIsStableAndRoundTrips(t *testing.T) {
	m := New("asset-1", 3)
	for i := 0; i < 3; i++ {
		if err := m.Add(validSegment(i)); err != nil {
			t.Fatalf("Add(%d): %v", i, err)
		}
	}
	m.CreatedAt = "2026-01-01T00:00:00Z"

	first, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("encoding is not deterministic")
	}

	decoded, err := Decode(first)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.AssetID != "asset-1" || decoded.SegmentCount != 3 || decoded.RotationCount != 0 {
		t.Fatalf("unexpected decoded manifest: %+v", decoded)
	}
	again, err := Encode(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) {
		t.Fatal("manifest does not survive an encode/decode/encode cycle")
	}
}

func TestAddRejectsDuplicateIndex(t *testing.T) {
	m := New("asset-1", 1)
	if err := m.Add(validSegment(0)); err != nil {
		t.Fatal(err)
	}
	err := m.Add(validSegment(0))
	if !errors.Is(err, ErrDuplicateIndex) {
		t.Fatalf("got %v, want ErrDuplicateIndex", err)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("duplicate add mutated the manifest: %d segments", len(m.Segments))
	}
}

func TestAddRejectsBadNonce(t *testing.T) {
	m := New("asset-1", 1)
	s := validSegment(0)
	s.Nonce = EncodeNonce([]byte("short"))
	if err := m.Add(s); !errors.Is(err, ErrBadNonceSize) {
		t.Fatalf("got %v, want ErrBadNonceSize", err)
	}
	s.Nonce = "not base64!"
	if err := m.Add(s); err == nil {
		t.Fatal("expected a malformed nonce to be rejected")
	}
}

func TestValidateRejectsStructuralProblems(t *testing.T) {
	m := New("asset-1", 1)
	for i := 0; i < 2; i++ {
		if err := m.Add(validSegment(i)); err != nil {
			t.Fatal(err)
		}
	}
	// Swap indices in place; this is what a corrupted manifest looks like once
	// decoded, since Decode sorts before validating.
	m.Segments[0], m.Segments[1] = m.Segments[1], m.Segments[0]
	if err := m.Validate(); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("got %v, want ErrOutOfOrder", err)
	}
}

func TestDecodeRejectsUnknownVersion(t *testing.T) {
	_, err := Decode([]byte(`{"format_version":99,"asset_id":"a","segments":[]}`))
	if err == nil {
		t.Fatal("expected unsupported format version to be rejected")
	}
}

func TestDecodeSurfacesMalformedSegments(t *testing.T) {
	if _, err := Decode([]byte(`{"format_version":1,"asset_id":"a","segment_count":1,"segments":[{}]}`)); err == nil {
		t.Fatal("expected an empty key id to be rejected")
	}
}

func TestNonceRoundTrip(t *testing.T) {
	raw := []byte("0123456789ab")
	enc := EncodeNonce(raw)
	got, err := DecodeNonce(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, got) {
		t.Fatalf("nonce round trip mismatch: %q != %q", raw, got)
	}
}

func TestSegmentLookup(t *testing.T) {
	m := New("asset-1", 1)
	for i := 0; i < 3; i++ {
		if err := m.Add(validSegment(i)); err != nil {
			t.Fatal(err)
		}
	}
	if s, ok := m.Segment(1); !ok || s.Index != 1 {
		t.Fatalf("lookup of index 1 failed: %+v %v", s, ok)
	}
	if _, ok := m.Segment(7); ok {
		t.Fatal("lookup of a missing index succeeded")
	}
	if n := m.Nonces(); len(n) != 3 {
		t.Fatalf("got %d nonces, want 3", len(n))
	}
}
