package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediad/internal/model"
)

// SourceFingerprint uniquely and immutably identifies one delivered source
// asset. It binds the raw bitstream bytes, the key-frame index and the codec
// into a single digest, so any change to either artifact changes the result.
type SourceFingerprint struct {
	Codec       string `json:"codec"`
	StreamSHA   string `json:"stream_sha256"`
	IndexSHA    string `json:"index_sha256"`
	StreamBytes int64  `json:"stream_bytes"`
	// SourceSHA is the final content identifier: SHA-256 over the canonical
	// encoding of the three fields above; it is what the archive indexes.
	SourceSHA string `json:"source_sha256"`
}

// hashBytes returns the lowercase hex SHA-256 of b.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// canonicalIndex serializes an index deterministically. JSON field order is
// fixed by struct definition and entries retain their validated order.
func canonicalIndex(idx *Index) ([]byte, error) {
	type entry struct {
		Offset int64 `json:"offset"`
		Length int64 `json:"length"`
		Key    bool  `json:"key"`
	}
	doc := struct {
		Codec   string  `json:"codec"`
		Entries []entry `json:"entries"`
	}{Codec: idx.Codec, Entries: make([]entry, len(idx.Entries))}
	for i, e := range idx.Entries {
		doc.Entries[i] = entry{Offset: e.Offset, Length: e.Length, Key: e.Key}
	}
	return json.Marshal(doc)
}

// FingerprintSource hashes the bitstream with the bounded chunk reader and
// combines the digest with the canonical index digest. The resulting source
// digest is stable across repeated deliveries of identical inputs.
func FingerprintSource(ctx context.Context, streamPath string, idx *Index, cfg ReaderConfig) (*SourceFingerprint, error) {
	if idx == nil {
		return nil, model.New(model.ErrStageState, "", -1, "cannot fingerprint a nil index")
	}
	sum, size, stats, err := HashFile(ctx, streamPath, cfg)
	if err != nil {
		return nil, err
	}
	if err := Guard(stats, cfg); err != nil {
		return nil, err
	}
	rawIndex, err := canonicalIndex(idx)
	if err != nil {
		return nil, err
	}
	fp := &SourceFingerprint{
		Codec:       idx.Codec,
		StreamSHA:   hex.EncodeToString(sum[:]),
		IndexSHA:    hashBytes(rawIndex),
		StreamBytes: size,
	}
	// Final digest binds codec (length-prefixed), stream digest and index
	// digest in one fixed layout; never via string concatenation.
	h := sha256.New()
	var lb [8]byte
	binary.BigEndian.PutUint32(lb[:4], uint32(len(fp.Codec)))
	h.Write(lb[:4])
	h.Write([]byte(fp.Codec))
	binary.BigEndian.PutUint64(lb[:], uint64(len(fp.StreamSHA)))
	h.Write(lb[:])
	h.Write([]byte(fp.StreamSHA))
	binary.BigEndian.PutUint64(lb[:], uint64(len(fp.IndexSHA)))
	h.Write(lb[:])
	h.Write([]byte(fp.IndexSHA))
	final := h.Sum(nil)
	fp.SourceSHA = hex.EncodeToString(final)
	return fp, nil
}

// FingerprintBytes is an in-memory variant used by tests and the fixture.
func FingerprintBytes(codec string, stream []byte, idx *Index) (*SourceFingerprint, error) {
	dir, err := os.MkdirTemp("", "mediad-fp-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "stream.bin")
	if err := os.WriteFile(path, stream, 0o600); err != nil {
		return nil, err
	}
	return FingerprintSource(context.Background(), path, idx, ReaderConfig{ChunkSize: 4096, QueueDepth: 2})
}

// DedupeTable records already-verified source digests. It is the single
// authority deciding whether a delivery may reuse prior results instead of
// appending a new manifest entry.
type DedupeTable struct {
	seen map[string]struct{}
}

// NewDedupeTable creates an empty dedupe table.
func NewDedupeTable() *DedupeTable { return &DedupeTable{seen: map[string]struct{}{}} }

// Seen reports whether the digest was already registered.
func (d *DedupeTable) Seen(sourceSHA string) bool {
	_, ok := d.seen[sourceSHA]
	return ok
}

// Register adds a digest after its artifacts have been committed. Registering
// an already-known digest returns a structured error: the caller must reuse
// the existing result rather than append a second manifest entry.
func (d *DedupeTable) Register(sourceSHA string) error {
	if d.Seen(sourceSHA) {
		return model.New(model.ErrDuplicateManifest, "", -1,
			"source %s already archived; reusing existing artifacts", sourceSHA)
	}
	d.seen[sourceSHA] = struct{}{}
	return nil
}

// Size returns the number of distinct registered sources.
func (d *DedupeTable) Size() int { return len(d.seen) }
