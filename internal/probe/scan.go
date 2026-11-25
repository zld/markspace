package probe

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/pion/rtp"

	"mediaflow/internal/clock"
	"mediaflow/internal/fault"
	"mediaflow/internal/policy"
)

// Sample is the small subset of an MP4 sample table needed for delivery decisions.
type Sample struct {
	Index    int    `json:"index"`
	Size     uint32 `json:"size"`
	Duration uint32 `json:"duration"`
	Offset   int64  `json:"offset"`
	IsSync   bool   `json:"is_sync"`
}

// Track contains parsed timing and sample-table facts for one supported track.
type Track struct {
	ID                 uint32         `json:"id"`
	Number             int            `json:"number"`
	Handler            string         `json:"handler"` // vide or soun
	Type               string         `json:"type"`    // video or audio
	Timescale          uint32         `json:"timescale"`
	DurationTicks      int64          `json:"duration_ticks"`
	Width              uint32         `json:"width"`
	Height             uint32         `json:"height"`
	Samples            []Sample       `json:"samples"`
	SyncSamples        map[int]bool   `json:"sync_samples,omitempty"`
	CompositionOffsets []int64        `json:"composition_offsets,omitempty"`
	Edits              []clock.Edit   `json:"edits,omitempty"`
	FirstTick          int64          `json:"first_tick"`
	LastTick           int64          `json:"last_tick"`
	Timeline           clock.Timeline `json:"timeline"`
}

// KeyframeGap describes a sparse sync-sample interval.
type KeyframeGap struct {
	StartSample int
	EndSample   int
	GapMS       int64
}

// Report is the complete scan result. Intervals may be non-empty even when Err is nil.
type Report struct {
	Path            string           `json:"path"`
	FileSize        int64            `json:"file_size"`
	InputDigest     string           `json:"input_digest"`
	LibraryBoxCount int              `json:"library_box_count"`
	Tracks          []*Track         `json:"tracks"`
	Intervals       []fault.Interval `json:"intervals,omitempty"`
	Stats           policy.Stats     `json:"stats"`
	Supported       bool             `json:"supported"`
}

// HasRejection reports whether any located interval makes the item undeliverable.
func (r *Report) HasRejection() bool {
	for _, in := range r.Intervals {
		if in.Reject {
			return true
		}
	}
	return false
}

// RejectionError converts rejecting intervals into an item error.
func (r *Report) RejectionError(itemID string) *fault.Error {
	if !r.HasRejection() {
		return nil
	}
	return &fault.Error{
		Class: fault.ClassUnsupported, ItemID: itemID, Path: r.Path,
		Message: "scan found one or more rejecting intervals", Intervals: r.Intervals,
	}
}

// DigestFile streams a file through SHA-256 without retaining its contents.
func DigestFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fault.Wrap(fault.ClassIO, err, "open input")
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, fault.Wrap(fault.ClassIO, err, "digest input")
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// RTPTimestampMS is a small adapter proving unified RTP/media timing use by scans.
func RTPTimestampMS(h rtp.Header, clockRate uint32) (int64, error) {
	return clock.RTPHeaderToMS(h, clockRate)
}

// FindTrack returns the first track matching id, or nil.
func (r *Report) FindTrack(id uint32) *Track {
	for _, t := range r.Tracks {
		if t.ID == id {
			return t
		}
	}
	return nil
}
