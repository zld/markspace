// Package media defines the shared data structures that flow between the
// fixture generator, key distributor, content encryption, license
// validation, pipeline and report modules.
package media

import (
	"fmt"
	"sync"
	"time"
)

// Track identifies a media track within an asset.
type Track string

const (
	TrackVideo Track = "video"
	TrackAudio Track = "audio"
)

// Segment is one generated media fragment.
type Segment struct {
	AssetID   string
	Track     Track
	Index     int
	PTS       int64
	Duration  int64
	Plaintext []byte
	Digest    [32]byte
}

// ID returns the stable segment identifier used in records and file names.
func (s Segment) ID() string {
	return fmt.Sprintf("%s-%04d", s.Track, s.Index)
}

// KeyInfo describes a data key without exposing any key material.
type KeyInfo struct {
	KeyID     string    `json:"key_id"`
	Epoch     int       `json:"epoch"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
}

// License is a locally signed entitlement for one segment. The signature is
// never serialized into reports.
type License struct {
	AssetID   string    `json:"asset_id"`
	Track     Track     `json:"track"`
	Index     int       `json:"index"`
	KeyID     string    `json:"key_id"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	Nonce     string    `json:"nonce"`
	Signature []byte    `json:"-"`
}

// Processing status values.
const (
	StatusAccepted = "accepted"
	StatusRejected = "rejected"
)

// Failure reasons reported per segment. ReasonNone means success.
const (
	ReasonNone             = ""
	ReasonReplay           = "replay_detected"
	ReasonKeyExpired       = "key_expired"
	ReasonUnknownKey       = "unknown_key"
	ReasonLicenseSignature = "license_invalid_signature"
	ReasonLicenseSegment   = "license_segment_mismatch"
	ReasonLicenseExpired   = "license_expired"
	ReasonAuthFailed       = "ciphertext_auth_failed"
	ReasonDigestMismatch   = "digest_mismatch"
)

// Record is the per-segment outcome emitted by the pipeline.
type Record struct {
	SegmentID     string `json:"segment_id"`
	KeyID         string `json:"key_id"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason"`
}

// Clock supplies the current time; implementations are controllable so
// expiry scenarios never depend on wall-clock waiting.
type Clock interface {
	Now() time.Time
}

// ManualClock is a deterministic Clock for demo runs and tests.
type ManualClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewManualClock returns a ManualClock pinned at t.
func NewManualClock(t time.Time) *ManualClock { return &ManualClock{t: t} }

// Now returns the current manual time.
func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward by d.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
