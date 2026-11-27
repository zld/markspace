// Package report renders the audit manifest: per-item input-to-output mapping,
// time/byte deltas, located failures and stable SHA-256 digests. Digests are
// computed over canonical JSON so rerunning identical inputs and parameters
// yields an identical manifest and summary.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mediasync/internal/config"
	"mediasync/internal/model"
)

// Status values are stable strings used in the manifest.
const (
	StatusOK       = "ok"
	StatusFailed   = "failed"
	StatusBlocked  = "blocked"
	StatusRejected = "rejected"
)

// Mapping is one input packet/slot to output timeline item relationship.
type Mapping struct {
	TrackID      string           `json:"track_id"`
	OutputSlot   int              `json:"output_slot"`
	Class        model.EntryClass `json:"class"`
	SourceOffset int              `json:"source_offset"`
	ExtSeq       uint64           `json:"ext_seq"`
	StartNS      model.NS         `json:"start_ns"`
	DurationNS   model.NS         `json:"duration_ns"`
	TimeDeltaNS  model.NS         `json:"time_delta_ns"`
	Bytes        int              `json:"bytes"`
	ByteDelta    int              `json:"byte_delta"`
	Status       string           `json:"status"`
}

// TrackReport is the full per-track audit record.
type TrackReport struct {
	TrackID  string            `json:"track_id"`
	Status   string            `json:"status"`
	Mappings []Mapping         `json:"mappings"`
	Failures []model.Failure   `json:"failures"`
	Decision *config.Decision  `json:"decision,omitempty"`
	Digest   string            `json:"digest"`
}

// Summary aggregates counts and the overall duration.
type Summary struct {
	TrackCount    int      `json:"track_count"`
	OKTracks      int      `json:"ok_tracks"`
	FailedTracks  int      `json:"failed_tracks"`
	BlockedTracks int      `json:"blocked_tracks"`
	SampleItems   int      `json:"sample_items"`
	GapItems      int      `json:"gap_items"`
	DuplicateItems int     `json:"duplicate_items"`
	FailureCount  int      `json:"failure_count"`
	TotalDuration model.NS `json:"total_duration_ns"`
}

// Manifest is the top-level deliverable.
type Manifest struct {
	Seed    int64         `json:"seed"`
	Params  Params        `json:"params"`
	Tracks  []TrackReport `json:"tracks"`
	Summary Summary       `json:"summary"`
	Digest  string        `json:"digest"`
}

// Params captures the run parameters that affect the output, so a manifest is
// only ever compared across identical parameter sets.
type Params struct {
	BudgetNS    model.NS `json:"budget_ns"`
	MaxBitrate  int64    `json:"max_bitrate_bps"`
	MinQuality  int      `json:"min_quality"`
}

// BuildTrackReport converts a timeline and optional decision into a report and
// computes its digest. blocked forces the blocked status regardless of data.
func BuildTrackReport(tl *model.TrackTimeline, decision *config.Decision, blocked bool) TrackReport {
	tr := TrackReport{TrackID: tl.TrackID, Decision: decision}
	for i, e := range tl.Entries {
		status := StatusOK
		if e.Failure != nil {
			status = StatusFailed
		}
		tr.Mappings = append(tr.Mappings, Mapping{
			TrackID:      tl.TrackID,
			OutputSlot:   i,
			Class:        e.Class,
			SourceOffset: e.SourceIndex,
			ExtSeq:       e.ExtSeq,
			StartNS:      e.Start,
			DurationNS:   e.Duration,
			TimeDeltaNS:  e.TimeDelta,
			Bytes:        e.Bytes,
			ByteDelta:    e.ByteDelta,
			Status:       status,
		})
	}
	tr.Failures = append(tr.Failures, tl.Failures...)
	if decision != nil {
		tr.Failures = append(tr.Failures, decision.Unsatisfiable...)
	}
	tr.Status = classify(blocked, tl, decision)
	tr.Digest = digestOf(struct {
		ID       string          `json:"track_id"`
		Mappings []Mapping       `json:"mappings"`
		Failures []model.Failure `json:"failures"`
		Decision *config.Decision `json:"decision"`
	}{tr.TrackID, tr.Mappings, tr.Failures, decision})
	return tr
}

func classify(blocked bool, tl *model.TrackTimeline, d *config.Decision) string {
	if blocked {
		return StatusBlocked
	}
	if d != nil && !d.Eligible {
		for _, f := range d.Unsatisfiable {
			if f.Code == model.ErrQualityBelowFloor && len(tl.Failures) == 0 {
				return StatusRejected
			}
		}
		return StatusRejected
	}
	if tl.HasFailure() {
		return StatusFailed
	}
	return StatusOK
}

// Build assembles the manifest, computes the summary and the overall digest.
// Track order is taken from the supplied slice so callers control ordering.
func Build(seed int64, params Params, reports []TrackReport) Manifest {
	m := Manifest{Seed: seed, Params: params, Tracks: reports}
	for _, tr := range reports {
		m.Summary.TrackCount++
		switch tr.Status {
		case StatusOK:
			m.Summary.OKTracks++
		case StatusBlocked:
			m.Summary.BlockedTracks++
		default:
			m.Summary.FailedTracks++
		}
		m.Summary.FailureCount += len(tr.Failures)
		for _, mp := range tr.Mappings {
			switch mp.Class {
			case model.EntrySample:
				m.Summary.SampleItems++
			case model.EntryGap:
				m.Summary.GapItems++
			case model.EntryDuplicate:
				m.Summary.DuplicateItems++
			}
			if end := mp.StartNS + mp.DurationNS; end > m.Summary.TotalDuration {
				m.Summary.TotalDuration = end
			}
		}
	}
	m.Digest = manifestDigest(seed, params, reports)
	return m
}

// manifestDigest folds each track digest with the seed and params. The hashed
// material is fully deterministic and independent of map iteration order.
func manifestDigest(seed int64, params Params, reports []TrackReport) string {
	h := sha256.New()
	write := func(b []byte) { h.Write(b); h.Write([]byte{0}) }
	write([]byte(hex64(seed)))
	write([]byte(digestOf(params)))
	ordered := append([]TrackReport(nil), reports...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].TrackID < ordered[j].TrackID })
	for _, tr := range ordered {
		write([]byte(tr.Digest))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func digestOf(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		// All hashed values are plain structs/slices; a marshal error is a bug.
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hex64(n int64) string {
	const digits = "0123456789abcdef"
	var buf [16]byte
	u := uint64(n)
	for i := 15; i >= 0; i-- {
		buf[i] = digits[u&0xf]
		u >>= 4
	}
	return string(buf[:])
}
