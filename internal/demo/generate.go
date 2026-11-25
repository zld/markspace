// Package demo generates all fixtures locally: DASH MPDs, HLS playlists
// (including discontinuity), valid and corrupted MP4 segments, length-
// framed RTP streams, and the per-segment expected hashes. No external
// files are referenced; every byte the scanner reads is produced here.
package demo

import (
	"os"
	"path/filepath"

	"mediascan/internal/fingerprint"
)

// FixtureRoot is the generated tree layout.
const (
	DirDASH   = "dash"
	DirHLS    = "hls"
	DirRTP    = "rtp"
	DirCorrupt = "corrupt"
	MPDName   = "manifest.mpd"
	M3U8Name  = "playlist.m3u8"
)

// Segment is one generated media segment and its expected identity.
type Segment struct {
	Path         string
	Source       string
	RepID        string
	SegmentIndex int64
	TrackID      uint32
	Timescale    uint32
	TimeStart    int64
	TimeEnd      int64
	Keyframe     bool
	// ExpectedHash is the SHA-256 the batch pipeline must reproduce.
	ExpectedHash string
	// ByteLength matches the hashed payload size.
	ByteLength int64
}

// Bundle is everything the generator wrote.
type Bundle struct {
	Root      string
	Segments  []Segment
	ManifestDASH []byte
	ManifestHLS  []byte
	RTPPath   string
	// Corrupt lists paths intentionally damaged with their first bad offset.
	Corrupt []CorruptFixture
}

// CorruptFixture records a damaged object and its locatable offset.
type CorruptFixture struct {
	Path       string
	BadOffset  int64
	Kind       string
}

// Generate writes the full fixture tree under root and returns the bundle.
// The layout is deterministic so repeated runs yield identical hashes.
func Generate(root string) (*Bundle, error) {
	if err := os.MkdirAll(filepath.Join(root, DirDASH), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, DirHLS), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, DirRTP), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, DirCorrupt), 0o755); err != nil {
		return nil, err
	}
	b := &Bundle{Root: root}
	if err := b.generateDASH(); err != nil {
		return nil, err
	}
	if err := b.generateHLS(); err != nil {
		return nil, err
	}
	if err := b.generateRTP(); err != nil {
		return nil, err
	}
	if err := b.generateCorrupt(); err != nil {
		return nil, err
	}
	return b, nil
}

// writeFile is a small helper kept private to the generator.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// expectedRecord renders the hash in the same shape used by the pipeline.
func expectedRecord(data []byte) string {
	return fingerprint.Sum(data)
}
