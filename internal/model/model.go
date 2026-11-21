// Package model defines the data structures shared across the local
// publish-verification pipeline: manifests, shards, licenses, reports and
// the injectable clock used to make expiry cases reproducible.
package model

import "time"

// Result status values.
const (
	StatusAccepted = "ACCEPTED"
	StatusRejected = "REJECTED"
)

// Failure reason values. The ordering of checks that produce these reasons
// is fixed and documented in package pipeline.
const (
	ReasonNone           = "NONE"
	ReasonKeyExpired     = "KEY_EXPIRED"
	ReasonUnknownKey     = "UNKNOWN_KEY"
	ReasonLicenseExpired = "LICENSE_EXPIRED"
	ReasonLicenseReplay  = "LICENSE_REPLAY"
	ReasonAuthTag        = "AUTH_TAG_MISMATCH"
	ReasonManifest       = "MANIFEST_MISMATCH"
	ReasonLength         = "LENGTH_MISMATCH"
)

// Clock is the injectable time source shared by the distributor and the
// license validator so that expiry scenarios are fully deterministic.
type Clock interface {
	Now() time.Time
}

// FixedClock always returns the same instant.
type FixedClock struct {
	T time.Time
}

// Now returns the fixed instant.
func (c FixedClock) Now() time.Time { return c.T }

// OffsetClock returns the base clock shifted by a fixed duration. It models
// a delayed shard whose local validation happens in the future (or past).
type OffsetClock struct {
	Base   Clock
	Offset time.Duration
}

// Now returns base time plus the configured offset.
func (c OffsetClock) Now() time.Time { return c.Base.Now().Add(c.Offset) }

// Shard describes one media shard as listed in the manifest. Plaintext is
// intentionally excluded from JSON serialization: on-disk bundles store
// plaintext in a separate file.
type Shard struct {
	ShardID        int    `json:"shard_id"`
	Size           int    `json:"size"`
	PlaintextHash  string `json:"plaintext_hash"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
}

// Manifest is the ordered list of shards belonging to one content package.
type Manifest struct {
	PackageID   string  `json:"package_id"`
	ShardSize   int     `json:"shard_size"`
	Digest      string  `json:"digest"`
	Shards      []Shard `json:"shards"`
	GeneratedAt string  `json:"generated_at"`
}

// License is the locally issued, HMAC-signed authorization for a shard.
// It never leaves the process and is not fetched from any authorization
// service.
type License struct {
	PackageID string `json:"package_id"`
	ShardID   int    `json:"shard_id"`
	ShardFrom int    `json:"shard_from"`
	ShardTo   int    `json:"shard_to"`
	Nonce     string `json:"nonce"`
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
	Sig       string `json:"sig"`
}

// ShardResult is the per-shard verification outcome stored in the report.
// Key material is never included.
type ShardResult struct {
	ShardID       int    `json:"shard_id"`
	KeyID         string `json:"key_id"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason"`
	Detail        string `json:"detail,omitempty"`
}

// Report is the full result of a pipeline run.
type Report struct {
	PackageID     string        `json:"package_id"`
	RotationCount int           `json:"rotation_count"`
	ShardCount    int           `json:"shard_count"`
	Accepted      int           `json:"accepted"`
	Rejected      int           `json:"rejected"`
	Results       []ShardResult `json:"results"`
}
