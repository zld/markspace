// Package manifest builds the auditable run manifest: track
// parameters, packet counters, sync deviation, recovery positions and
// input digests, serialized deterministically and hashed with SHA-256.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"

	"continuity/internal/media"
	"continuity/internal/rtp"
)

// RTPStats is the serializable packet-level summary.
type RTPStats struct {
	Received   int         `json:"received"`
	Lost       int         `json:"lost"`
	Duplicated int         `json:"duplicated"`
	Late       int         `json:"late"`
	Missing    []rtp.Range `json:"missing_ranges"`
}

// Recovery marks one recovery position on the program timeline.
type Recovery struct {
	Seq         uint64 `json:"seq"`
	TimestampNs int64  `json:"timestamp_ns"`
}

// SyncSummary carries the A/V alignment result.
type SyncSummary struct {
	Pairs          int   `json:"pairs"`
	MaxDeviationNs int64 `json:"max_deviation_ns"`
}

// InputDigest pins the processed inputs.
type InputDigest struct {
	MatroskaSHA256 string `json:"matroska_sha256"`
	MatroskaBytes  int    `json:"matroska_bytes"`
	MP4SHA256      string `json:"mp4_sha256"`
	MP4Bytes       int    `json:"mp4_bytes"`
	RTPSHA256      string `json:"rtp_sha256"`
	RTPPackets     int    `json:"rtp_packets"`
}

// Manifest is the deterministic run record. Field order is fixed by
// the struct declaration, so Bytes is stable across runs.
type Manifest struct {
	Seed        int64               `json:"seed"`
	Tracks      []media.TrackParams `json:"tracks"`
	Decisions   []media.Decision    `json:"decisions"`
	RTP         RTPStats            `json:"rtp"`
	Recovery    []Recovery          `json:"recovery_positions"`
	Sync        SyncSummary         `json:"sync"`
	Input       InputDigest         `json:"input"`
	Events      int                 `json:"events"`
	EventDigest string              `json:"event_digest"`
	Termination string              `json:"termination,omitempty"`
}

// Bytes serializes the manifest deterministically.
func (m *Manifest) Bytes() []byte {
	b, _ := json.MarshalIndent(m, "", "  ")
	return b
}

// SHA256 hex-hashes serialized content.
func SHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Write persists the manifest atomically and returns its SHA-256.
func Write(path string, m *Manifest) (string, error) {
	b := m.Bytes()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return SHA256(b), nil
}
