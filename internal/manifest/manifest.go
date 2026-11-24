// Package manifest builds the per-task time manifest. Every entry records
// start/end time, recovery status, A/V deviation and error codes. Manifest
// timing must track the media boundary within one video frame period or
// 10 ms, whichever is larger.
package manifest

import (
	"encoding/json"
	"fmt"

	"mediacheck/internal/fault"
)

// BoundaryToleranceNanos is the absolute floor for manifest boundary error.
const BoundaryToleranceNanos = int64(10_000_000) // 10 ms

// Status values for manifest entries.
const (
	StatusOK          = "ok"
	StatusRecovered   = "recovered"
	StatusUnrecovered = "unrecovered"
	StatusDesync      = "desync"
)

// Entry is one manifest line.
type Entry struct {
	Index        int          `json:"index"`
	StartNanos   int64        `json:"start_nanos"`
	EndNanos     int64        `json:"end_nanos"`
	Status       string       `json:"status"`
	AVDeltaNanos int64        `json:"av_delta_nanos"`
	Errors       []fault.Code `json:"errors,omitempty"`
}

// Manifest is the full per-task manifest.
type Manifest struct {
	Source  string        `json:"source"`
	Entries []Entry       `json:"entries"`
	Faults  []fault.Fault `json:"faults,omitempty"`
}

// Add appends an entry; indexes are assigned sequentially.
func (m *Manifest) Add(e Entry) {
	e.Index = len(m.Entries)
	m.Entries = append(m.Entries, e)
}

// BoundaryError returns the worst absolute deviation between the manifest
// extent and the media extent [0, mediaDurationNanos].
func (m *Manifest) BoundaryError(mediaDurationNanos int64) int64 {
	if len(m.Entries) == 0 {
		return mediaDurationNanos
	}
	startErr := m.Entries[0].StartNanos
	if startErr < 0 {
		startErr = -startErr
	}
	endErr := m.Entries[len(m.Entries)-1].EndNanos - mediaDurationNanos
	if endErr < 0 {
		endErr = -endErr
	}
	if endErr > startErr {
		return endErr
	}
	return startErr
}

// Validate checks the manifest extent against the media duration. The
// allowed error is max(frameDurNanos, 10ms).
func (m *Manifest) Validate(mediaDurationNanos, frameDurNanos int64) error {
	limit := frameDurNanos
	if limit < BoundaryToleranceNanos {
		limit = BoundaryToleranceNanos
	}
	if err := m.BoundaryError(mediaDurationNanos); err > limit {
		return fmt.Errorf("manifest boundary error %d ns exceeds limit %d ns", err, limit)
	}
	return nil
}

// JSON renders the manifest deterministically (struct field order).
func (m *Manifest) JSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}
