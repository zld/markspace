package stream

import (
	"sort"
	"sync"

	"mediapack/ingest"
	"mediapack/model"
)

// TrackStatus describes how a track ended after damage handling.
type TrackStatus string

const (
	TrackComplete        TrackStatus = "complete"
	TrackRecoveredPrefix TrackStatus = "recovered_prefix"
	TrackSkippedUnits    TrackStatus = "skipped_units"
	TrackStoppedUnsafe   TrackStatus = "stopped_unsafe"
)

// TrackRecovery is the auditable recovery state for one track.
type TrackRecovery struct {
	Track              model.TrackID `json:"track"`
	Status             TrackStatus   `json:"status"`
	SafeOffset         int64         `json:"safe_offset"`
	SkippedUnits       int           `json:"skipped_units"`
	UnrecoveredPackets int           `json:"unrecovered_packets"`
	ReorderedRecovered int           `json:"reordered_recovered"`
	Reason             string        `json:"reason,omitempty"`
}

// RecoveryManager aggregates RTP gaps, skipped bad units, and safe MP4 prefixes.
type RecoveryManager struct {
	mu       sync.Mutex
	tracks   map[model.TrackID]*TrackRecovery
	warnings []model.Warning
	fatal    []*model.Error
}

// NewRecoveryManager creates an empty manager.
func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{tracks: make(map[model.TrackID]*TrackRecovery)}
}

func (m *RecoveryManager) track(track model.TrackID) *TrackRecovery {
	if m.tracks[track] == nil {
		m.tracks[track] = &TrackRecovery{Track: track, Status: TrackComplete, SafeOffset: -1}
	}
	return m.tracks[track]
}

// AddWarning records a non-fatal warning.
func (m *RecoveryManager) AddWarning(w model.Warning) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.warnings = append(m.warnings, w)
	if w.Track != "" {
		tr := m.track(w.Track)
		if w.Category == model.CategoryIntegrityMismatch {
			tr.SkippedUnits++
			if tr.Status == TrackComplete {
				tr.Status = TrackSkippedUnits
			}
		}
	}
}

// AddRTPStats merges sequence recovery counters for a track.
func (m *RecoveryManager) AddRTPStats(stats ingest.RTPStats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tr := m.track(stats.Track)
	tr.UnrecoveredPackets += stats.UnrecoveredPackets
	tr.ReorderedRecovered += stats.ReorderedRecovered
}

// HandleMediaError converts a categorized input error into a recovery decision.
// Recoverable truncation preserves the already verified prefix. Non-recoverable
// framing errors stop only the affected track.
func (m *RecoveryManager) HandleMediaError(err *model.Error) TrackRecovery {
	m.mu.Lock()
	defer m.mu.Unlock()
	tr := m.track(err.Track)
	tr.SafeOffset = err.Offset
	tr.Reason = string(err.Category) + ": " + err.Message
	if err.Recoverable {
		tr.Status = TrackRecoveredPrefix
		m.warnings = append(m.warnings, model.Warning{
			Track: err.Track, Offset: err.Offset, Category: err.Category, Message: err.Error(),
		})
	} else {
		tr.Status = TrackStoppedUnsafe
		m.fatal = append(m.fatal, err)
	}
	return *tr
}

// Complete marks a track fully consumed if no stronger recovery state exists.
func (m *RecoveryManager) Complete(track model.TrackID, endOffset int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tr := m.track(track)
	tr.SafeOffset = endOffset
	if tr.Status == TrackComplete {
		tr.Status = TrackComplete
	}
}

// Warnings returns a stable copy of warnings.
func (m *RecoveryManager) Warnings() []model.Warning {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.Warning(nil), m.warnings...)
}

// Fatal returns non-recoverable track errors.
func (m *RecoveryManager) Fatal() []*model.Error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*model.Error(nil), m.fatal...)
}

// Tracks returns recovery states sorted by track ID for stable JSON/reports.
func (m *RecoveryManager) Tracks() []TrackRecovery {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TrackRecovery, 0, len(m.tracks))
	for _, tr := range m.tracks {
		out = append(out, *tr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Track < out[j].Track })
	return out
}

// Totals returns aggregate loss and skip counts.
func (m *RecoveryManager) Totals() (unrecovered int, skipped int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, tr := range m.tracks {
		unrecovered += tr.UnrecoveredPackets
		skipped += tr.SkippedUnits
	}
	return unrecovered, skipped
}
