// Package fingerprint computes the three independent digests mediavault uses:
// sample content, chunked sample content and container bytes. Identity never
// depends on a path or modification time.
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/model"
)

// PolicyVersion is bumped whenever chunking or digest framing changes. It is
// stored beside every digest so a policy change invalidates derived products
// without touching the source bytes.
const PolicyVersion = "pcm-shard-v1"

// Policy describes how frames are grouped into blocks.
type Policy struct {
	Version     string
	ChunkFrames int64
}

// DefaultPolicy is the acceptance policy: 1 KiB blocks, sized in frames so
// that every block is frame aligned and bounded well below the 4 MiB working
// memory envelope.
func DefaultPolicy(channels, bits int) Policy {
	frameBytes := channels * ((bits + 7) / 8)
	if frameBytes <= 0 {
		frameBytes = 1
	}
	frames := int64(1024 / frameBytes)
	if frames < 1 {
		frames = 1
	}
	return Policy{Version: PolicyVersion, ChunkFrames: frames}
}

const domainSeparator = "mediavault\x00"

// ContentHasher incrementally digests interleaved PCM frame bytes.
type ContentHasher struct {
	h hash.Hash
}

// NewContentHasher starts a sample-content digest.
func NewContentHasher() *ContentHasher { return &ContentHasher{h: sha256.New()} }

// Write adds raw frame bytes.
func (c *ContentHasher) Write(p []byte) (int, error) { return c.h.Write(p) }

// Sum returns the lowercase 64-character hex digest.
func (c *ContentHasher) Sum() string { return hex.EncodeToString(c.h.Sum(nil)) }

// BlobDigest is the exact SHA-256 content address of a byte slice. Objects in
// the store are addressed by this value, so the same samples stored under two
// assets collapse to one object.
func BlobDigest(p []byte) string {
	sum := sha256.Sum256(p)
	return hex.EncodeToString(sum[:])
}

// ChunkDigest is the content address of one derived block. Identity is the
// block bytes alone; the block index and frame count are framed into the asset
// Aggregate so reordering blocks cannot alias a different layout.
func ChunkDigest(index int, frames int64, pcm []byte) string {
	return BlobDigest(pcm)
}

// ContainerDigest digests only the wrapper bytes (RIFF header/chunks that are
// not sample data, or the full MP4 box tree). Truncated to 32 bytes of header
// so that a container-only edit can be shown to leave PCM products reusable.
func ContainerDigest(parts ...[]byte) string {
	h := sha256.New()
	h.Write([]byte(domainSeparator + "container\x00"))
	for _, p := range parts {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Aggregate digests the ordered chunk digests plus policy and format facts.
// This is the content address of the derived PCM asset and the value stored as
// the asset's ContentDigest.
func Aggregate(policy Policy, format model.WAVFormat, chunks []model.ChunkRef) (string, error) {
	if policy.Version == "" || policy.ChunkFrames <= 0 {
		return "", fault.New(fault.CodePolicyVersion, fault.StageFingerprinted, -1, "invalid policy %q/%d", policy.Version, policy.ChunkFrames)
	}
	h := sha256.New()
	h.Write([]byte(domainSeparator + "aggregate\x00"))
	writeField(h, policy.Version)
	writeField(h, format.SampleRate.Num, format.SampleRate.Den)
	writeField(h, int64(format.Channels), int64(format.BitsPerSample))
	writeField(h, policy.ChunkFrames)
	for _, c := range chunks {
		writeField(h, int64(c.Index), c.Frames, c.Length)
		writeField(h, c.Digest)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeField(h hash.Hash, vals ...any) {
	for _, v := range vals {
		var buf [8]byte
		switch x := v.(type) {
		case int64:
			binary.BigEndian.PutUint64(buf[:], uint64(x))
			h.Write(buf[:])
		case uint64:
			binary.BigEndian.PutUint64(buf[:], x)
			h.Write(buf[:])
		case string:
			binary.BigEndian.PutUint64(buf[:], uint64(len(x)))
			h.Write(buf[:])
			h.Write([]byte(x))
		}
	}
}

// ValidDigest reports whether s is a lowercase or uppercase 64-char hex string.
func ValidDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// SamePCM reports whether two chunk sets describe identical sample content.
// Only the chunk digests participate, so a container-only change is reusable.
func SamePCM(a, b []model.ChunkRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Index != b[i].Index || a[i].Digest != b[i].Digest || a[i].Frames != b[i].Frames {
			return false
		}
	}
	return true
}

// ChangedChunks returns the indexes whose digests differ or that exist on only
// one side. Blocks outside this set are reusable.
func ChangedChunks(prev, next []model.ChunkRef) []int {
	old := make(map[int]string, len(prev))
	for _, c := range prev {
		old[c.Index] = c.Digest
	}
	var changed []int
	seen := make(map[int]bool, len(next))
	for _, c := range next {
		seen[c.Index] = true
		if d, ok := old[c.Index]; !ok || d != c.Digest {
			changed = append(changed, c.Index)
		}
	}
	for i := range old {
		if !seen[i] {
			changed = append(changed, i)
		}
	}
	sortInts(changed)
	return changed
}

func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
