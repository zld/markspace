package mp4

import (
	"errors"

	"mediad/internal/clock"
)

// Limits protect against untrusted sample tables.
const (
	// MaxSamplesPerTrack bounds memory: each sample costs a few dozen bytes.
	MaxSamplesPerTrack = 2_000_000
	// MaxTracks bounds per-file exploration.
	MaxTracks = 64
	// MaxMdatBytes caps the declared media payload we are willing to read.
	MaxMdatBytes = int64(64) << 30 // 64 GiB
	// MaxEditEntries bounds elst parsing.
	MaxEditEntries = 256
)

// RawSample preserves source byte offset and stored timing exactly.
type RawSample struct {
	Index  uint32 `json:"index"` // 1-based sample number as stored
	Offset uint64 `json:"offset"`
	Size   uint32 `json:"size"`
	DTS    int64  `json:"dts"`
	PTS    int64  `json:"pts"`
	Sync   bool   `json:"sync"`
	// TableOffset is the byte position of the timing entry that produced DTS,
	// used to point reviewers at the exact bytes on failure.
	TableOffset int64 `json:"table_offset"`
}

// RawTrack is a scanned track before repair.
type RawTrack struct {
	TrackID        uint32         `json:"track_id"`
	Kind           string         `json:"kind"` // video/audio/other
	Timescale      uint32         `json:"timescale"`
	DurationTicks  int64          `json:"duration_ticks"`
	MovieTimescale uint32         `json:"movie_timescale"`
	HandlerOffset  int64          `json:"handler_offset"`
	Samples        []RawSample    `json:"samples"`
	EditList       clock.EditList `json:"edit_list"`
	// HasSyncTable distinguishes "no sync samples" from "stss absent".
	HasSyncTable bool `json:"has_sync_table"`
	// Problems are non-fatal anomalies discovered during scanning.
	Problems []Problem `json:"problems,omitempty"`
	// TableBytes records where each table lives for provenance.
	TableBytes TableBytes `json:"table_bytes"`
}

// TableBytes locates the sample tables in the source file.
type TableBytes struct {
	Stts int64 `json:"stts"`
	Stsz int64 `json:"stsz"`
	Stco int64 `json:"stco"`
	Stsc int64 `json:"stsc"`
	Stss int64 `json:"stss"`
	Elst int64 `json:"elst"`
	Mdat int64 `json:"mdat"`
}

// Problem is a non-fatal anomaly such as a missing sync table.
type Problem struct {
	Code   clock.Code `json:"code"`
	Detail string     `json:"detail"`
	Offset int64      `json:"raw_offset"`
}

// Scan is the full result for one file.
type Scan struct {
	// Path is the source location. It is excluded from delivery digests
	// (json:"-") because moving identical bytes must not change the digest.
	Path       string     `json:"-"`
	Size       int64      `json:"size"`
	MovieScale uint32     `json:"movie_timescale"`
	Tracks     []RawTrack `json:"tracks"`
	Problems   []Problem  `json:"problems,omitempty"`
}

// ErrUnsafeLength marks length fields that exceed configured bounds.
var ErrUnsafeLength = errors.New("unsafe length")
