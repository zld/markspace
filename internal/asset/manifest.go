// Package asset defines the media asset manifest, its segment metadata and a
// deterministic serialization format.
//
// The manifest is the only description of an encrypted asset that leaves the
// packaging step. It deliberately contains no key material: it records the
// key identifier used for each segment, the segment nonce, the plaintext
// digest and the ciphertext digest so that validation can classify failures
// without ever mutating the asset.
package asset

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const (
	// FormatVersion identifies the manifest schema.
	FormatVersion = 1
	// NonceSize is the AES-GCM nonce size in bytes.
	NonceSize = 12
	// KeySize is the AES-256 key size in bytes.
	KeySize = 32
)

// Errors returned by manifest validation. Callers map these to stable reason
// codes in the validation pipeline.
var (
	ErrEmptyAssetID    = errors.New("manifest: asset id is empty")
	ErrAssetIDMismatch = errors.New("manifest: asset id mismatch")
	ErrNoSegments      = errors.New("manifest: no segments")
	ErrSegmentCount    = errors.New("manifest: segment count mismatch")
	ErrDuplicateIndex  = errors.New("manifest: duplicate segment index")
	ErrOutOfOrder      = errors.New("manifest: segment order is not ascending")
	ErrEmptyKeyID      = errors.New("manifest: empty key id")
	ErrBadNonceSize    = errors.New("manifest: bad nonce size")
	ErrBadDigest       = errors.New("manifest: malformed digest")
)

// Segment is the metadata recorded for one encrypted media segment.
type Segment struct {
	Index      int    `json:"index"`
	KeyID      string `json:"key_id"`
	KeyVersion int    `json:"key_version"`
	// Nonce is the standard-base64 encoded AES-GCM nonce exactly as used for
	// encryption. It is random in production and derived deterministically in
	// seeded demo mode.
	Nonce string `json:"nonce"`
	// PlainDigest is the hex SHA-256 of the plaintext, used to classify
	// authentication failures caused by corrupted ciphertext.
	PlainDigest string `json:"plain_digest"`
	// CipherDigest is the hex SHA-256 of the complete GCM output, including
	// the authentication tag.
	CipherDigest string `json:"cipher_digest"`
	// PlainSize is the plaintext length in bytes and CipherSize the complete
	// sealed length (plaintext + GCM tag).
	PlainSize  int `json:"plain_size"`
	CipherSize int `json:"cipher_size"`
}

// Manifest describes one encrypted media asset.
//
// Segments are ordered by Index. The JSON encoding of a manifest is stable:
// struct field order is fixed by this declaration and segments are kept in
// ascending index order, so the same manifest always serializes to the same
// bytes.
type Manifest struct {
	FormatVersion int       `json:"format_version"`
	AssetID       string    `json:"asset_id"`
	RotateEvery   int       `json:"rotate_every"`
	RotationCount int       `json:"rotation_count"`
	SegmentCount  int       `json:"segment_count"`
	CreatedAt     string    `json:"created_at"`
	Segments      []Segment `json:"segments"`
}

// New returns an empty manifest for assetID.
func New(assetID string, rotateEvery int) *Manifest {
	return &Manifest{
		FormatVersion: FormatVersion,
		AssetID:       assetID,
		RotateEvery:   rotateEvery,
		Segments:      make([]Segment, 0, 16),
	}
}

// Add appends s, rejecting duplicate indices so that an existing segment is
// never silently overwritten. It records the highest key version seen as the
// rotation count.
func (m *Manifest) Add(s Segment) error {
	if s.Index < 0 {
		return fmt.Errorf("manifest: negative segment index %d", s.Index)
	}
	if s.KeyID == "" {
		return ErrEmptyKeyID
	}
	if raw, err := DecodeNonce(s.Nonce); err != nil {
		return err
	} else if len(raw) != NonceSize {
		return ErrBadNonceSize
	}
	if _, err := hex.DecodeString(s.PlainDigest); err != nil || len(s.PlainDigest) != 64 {
		return fmt.Errorf("%w: plain_digest", ErrBadDigest)
	}
	if _, err := hex.DecodeString(s.CipherDigest); err != nil || len(s.CipherDigest) != 64 {
		return fmt.Errorf("%w: cipher_digest", ErrBadDigest)
	}
	for _, existing := range m.Segments {
		if existing.Index == s.Index {
			return fmt.Errorf("%w: index %d", ErrDuplicateIndex, s.Index)
		}
	}
	m.Segments = append(m.Segments, s)
	sort.Slice(m.Segments, func(i, j int) bool { return m.Segments[i].Index < m.Segments[j].Index })
	m.SegmentCount = len(m.Segments)
	// Version 1 is the initial key, so N rotations mean the highest version
	// in use is N+1.
	if rotations := s.KeyVersion - 1; rotations > m.RotationCount {
		m.RotationCount = rotations
	}
	return nil
}

// Segment returns the segment with index i and whether it exists.
func (m *Manifest) Segment(i int) (Segment, bool) {
	idx := sort.Search(len(m.Segments), func(k int) bool { return m.Segments[k].Index >= i })
	if idx < len(m.Segments) && m.Segments[idx].Index == i {
		return m.Segments[idx], true
	}
	return Segment{}, false
}

// Validate checks the structural invariants of a manifest: a non-empty asset
// id, at least one segment, contiguous ascending indices starting at zero and
// well-formed per-segment metadata. It does not validate any key material.
func (m *Manifest) Validate() error {
	if m.AssetID == "" {
		return ErrEmptyAssetID
	}
	if len(m.Segments) == 0 {
		return ErrNoSegments
	}
	if m.SegmentCount != 0 && m.SegmentCount != len(m.Segments) {
		return fmt.Errorf("%w: declared %d, found %d", ErrSegmentCount, m.SegmentCount, len(m.Segments))
	}
	seen := make(map[int]struct{}, len(m.Segments))
	for i, s := range m.Segments {
		if _, dup := seen[s.Index]; dup {
			return fmt.Errorf("%w: index %d", ErrDuplicateIndex, s.Index)
		}
		seen[s.Index] = struct{}{}
		if s.Index != i {
			return fmt.Errorf("%w: position %d holds index %d", ErrOutOfOrder, i, s.Index)
		}
		if s.KeyID == "" {
			return fmt.Errorf("%w: index %d", ErrEmptyKeyID, s.Index)
		}
		raw, err := DecodeNonce(s.Nonce)
		if err != nil {
			return fmt.Errorf("manifest: index %d: %w", s.Index, err)
		}
		if len(raw) != NonceSize {
			return fmt.Errorf("%w: index %d", ErrBadNonceSize, s.Index)
		}
		if len(s.PlainDigest) != 64 || len(s.CipherDigest) != 64 {
			return fmt.Errorf("%w: index %d", ErrBadDigest, s.Index)
		}
	}
	return nil
}

// Nonces maps each manifest nonce to the segment index that owns it. It is
// used during packaging to detect a nonce collision before any overwrite.
func (m *Manifest) Nonces() map[string]int {
	out := make(map[string]int, len(m.Segments))
	for _, s := range m.Segments {
		out[s.Nonce] = s.Index
	}
	return out
}

// Encode serializes the manifest deterministically. Go's encoding/json emits
// struct fields in declaration order, so the byte representation is stable
// across runs for an identical manifest.
func Encode(m *Manifest) ([]byte, error) {
	if m == nil {
		return nil, errors.New("manifest: nil")
	}
	return json.MarshalIndent(m, "", "  ")
}

// Decode parses an encoded manifest. The result is validated structurally.
func Decode(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: decode: %w", err)
	}
	if m.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("manifest: unsupported format version %d", m.FormatVersion)
	}
	if m.Segments == nil {
		m.Segments = []Segment{}
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// EncodeNonce returns the standard-base64 form of a raw nonce.
func EncodeNonce(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

// DecodeNonce parses a standard-base64 nonce as stored in a manifest.
func DecodeNonce(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("manifest: malformed nonce: %w", err)
	}
	return raw, nil
}

// Digest is a convenience helper returning the hex SHA-256 of b.
func Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
