// Package pipeline wires every module together: fixture/manifest input,
// license verification, key dispatch, chunk crypto, replay protection and
// report aggregation. A failure on one chunk never aborts the others.
package pipeline

import (
	"crypto/rand"
	"io"
	"sync"
	"time"

	"mediakey/internal/keys"
	"mediakey/internal/license"
	"mediakey/internal/manifest"
	"mediakey/internal/replay"
)

// EncryptConfig parameterizes an encryption run over a plaintext buffer.
type EncryptConfig struct {
	AssetID     string
	ChunkSize   int
	RotateEvery int
	Plaintext   []byte
	RootKey     []byte // optional; derived from the demo seed when nil
	Rand        io.Reader // defaults to crypto/rand
}

// VerifyConfig parameterizes a verification/demo run against a manifest.
type VerifyConfig struct {
	Manifest     *manifest.Manifest
	RootKey      []byte
	SigningKey   []byte
	Lic          *license.License
	Now          time.Time
	GraceWindow  time.Duration
	CacheCap     int
	CacheWindow  time.Duration
	Workers      int
	// Per-chunk fault injections keyed by chunk index.
	Faults map[int]string
	// NonceFor optionally supplies deterministic nonces (demo mode).
	NonceFor func(chunkIndex int) string
}

// Runtime bundles the stateful collaborators of a verify run.
type Runtime struct {
	dispatcher *keys.Dispatcher
	cache      *replay.Cache
	lic        *license.License
	signingKey []byte
	now        func() time.Time
	grace      time.Duration
	nonceFor   func(int) string
	faults     map[int]string
	seq        int64
	seqMu      sync.Mutex
}

func defaultRand(r io.Reader) io.Reader {
	if r == nil {
		return rand.Reader
	}
	return r
}

// sanityLimits rejects obviously dangerous inputs before any allocation.
func sanityLimits(chunkSize, chunks int) error {
	if chunkSize <= 0 || chunkSize > manifest.MaxChunkBytes {
		return manifest.NewError(manifest.ReasonLimitExceeded, "chunk size out of allowed range")
	}
	if chunks <= 0 || chunks > manifest.MaxChunks {
		return manifest.NewError(manifest.ReasonLimitExceeded, "chunk count out of allowed range")
	}
	return nil
}

// demoKeyIDs exposes the key-id naming used in reports.
func demoKeyID(round int) string { return keys.KeyID(round) }
