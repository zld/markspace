// Package fingerprint computes stable content fingerprints and marks duplicate
// relationships without mutating or deleting source assets.
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"sort"

	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/trace"
)

// ModuleVersion participates in derived cache keys.
const ModuleVersion = "fingerprint/v1"

// Record is one fingerprinted source range or derived interval.
type Record struct {
	Key          string `json:"key"`
	SegmentIndex int    `json:"segment_index"`
	TrackID      int    `json:"track_id"`
	Digest       string `json:"digest"`
}

// DuplicateGroup links equal content fingerprints.
type DuplicateGroup struct {
	Digest string   `json:"digest"`
	Keys   []string `json:"keys"`
}

// Bytes returns a stable SHA-256 fingerprint of length-framed chunks. Length
// framing prevents concatenation ambiguity.
func Bytes(chunks ...[]byte) string {
	h := sha256.New()
	var lenBuf [8]byte
	for _, chunk := range chunks {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(chunk)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(chunk)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Samples fingerprints only media payload bytes, so the same payload on
// another track/node receives the same digest.
func Samples(samples ...*model.Sample) string {
	chunks := make([][]byte, 0, len(samples))
	for _, s := range samples {
		if s != nil {
			chunks = append(chunks, s.Data)
		}
	}
	return Bytes(chunks...)
}

// MarkDuplicates groups records with equal digests. It only reports relations;
// callers retain every source asset.
func MarkDuplicates(records []Record) []DuplicateGroup {
	byDigest := make(map[string][]string)
	for _, r := range records {
		if r.Digest == "" {
			continue
		}
		byDigest[r.Digest] = append(byDigest[r.Digest], r.Key)
	}
	groups := make([]DuplicateGroup, 0)
	for digest, keys := range byDigest {
		if len(keys) < 2 {
			continue
		}
		sort.Strings(keys)
		groups = append(groups, DuplicateGroup{Digest: digest, Keys: keys})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Digest < groups[j].Digest })
	return groups
}

// VerifyCRC checks an index-provided CRC at the fingerprint stage. A mismatch
// is a unified PAYLOAD_DIGEST_MISMATCH error located at byteOffset, which
// callers set to the known injected/first corrupt byte when available.
func VerifyCRC(expected uint32, data []byte, trackID int, byteOffset int64) error {
	actual := crc32.ChecksumIEEE(data)
	if actual == expected {
		return nil
	}
	err := trace.E(
		trace.CodePayloadMismatch,
		trace.StageFingerprint,
		"payload CRC32 mismatch: expected %08x actual %08x",
		expected, actual,
	).WithTrack(trackID)
	if byteOffset >= 0 {
		err = err.WithOffset(byteOffset)
	}
	return err
}
