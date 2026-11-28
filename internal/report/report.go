// Package report renders validation results. Serialization is fully
// deterministic: faults are sorted by (offset, code, track) and the content
// hash is computed over the canonical bytes, so identical inputs always
// produce identical hashes and error ordering across runs.
package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mediasync/internal/fault"
)

// Exit codes defined by the command contract.
const (
	ExitOK      = 0 // all checks passed
	ExitRuntime = 1 // operational error or cancellation
	ExitInvalid = 2 // media validation failed
)

// FaultView is the serializable form of a fault.
type FaultView struct {
	Code   int    `json:"code"`
	Name   string `json:"error_code"`
	Track  string `json:"track"`
	Offset int64  `json:"offset"`
	Detail string `json:"detail"`
}

// Metrics holds the headline numbers printed for every run.
type Metrics struct {
	SyncErrorMS   float64 `json:"sync_error_ms"`
	LossRecovered int     `json:"loss_recovered"`
	LossTotal     int     `json:"loss_total"`
	LatencyP95MS  float64 `json:"latency_p95_ms"`
}

// RecoveryRate returns recovered/total as a percentage in [0,100]; 100 when
// there was no loss at all.
func (m Metrics) RecoveryRate() float64 {
	if m.LossTotal == 0 {
		return 100
	}
	return 100 * float64(m.LossRecovered) / float64(m.LossTotal)
}

// Report is the canonical output document.
type Report struct {
	Metrics Metrics     `json:"metrics"`
	Faults  []FaultView `json:"faults"`
	// ContentHash is over the canonical JSON of Metrics+Faults only.
	ContentHash string `json:"content_hash"`
}

// FromFaults converts domain faults into sorted, deduplicated views. A nil
// input yields an empty (but non-nil) slice so JSON output is stable.
func FromFaults(faults []*fault.Fault) []FaultView {
	views := make([]FaultView, 0, len(faults))
	for _, f := range faults {
		if f == nil {
			continue
		}
		views = append(views, FaultView{
			Code:   int(f.Code),
			Name:   f.Code.String(),
			Track:  f.Track,
			Offset: f.Offset,
			Detail: f.Detail,
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Offset != views[j].Offset {
			return views[i].Offset < views[j].Offset
		}
		if views[i].Code != views[j].Code {
			return views[i].Code < views[j].Code
		}
		if views[i].Track != views[j].Track {
			return views[i].Track < views[j].Track
		}
		return views[i].Detail < views[j].Detail
	})
	return views
}

// canonical builds the exact bytes that define identity: metrics followed by
// the sorted fault views. No map iteration is involved, so output is stable.
func canonical(metrics Metrics, views []FaultView) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		Metrics Metrics     `json:"metrics"`
		Faults  []FaultView `json:"faults"`
	}{metrics, views}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Build assembles a report and computes its content hash deterministically.
func Build(metrics Metrics, faults []*fault.Fault) (*Report, error) {
	views := FromFaults(faults)
	raw, err := canonical(metrics, views)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return &Report{
		Metrics:     metrics,
		Faults:      views,
		ContentHash: hex.EncodeToString(sum[:]),
	}, nil
}

// RenderJSON emits the full report as canonical, indented JSON for terminals.
func (r *Report) RenderJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// HasValidationFaults reports whether any fault is a media-validation failure
// (as opposed to an operational/cancel fault).
func (r *Report) HasValidationFaults() bool {
	for _, f := range r.Faults {
		if f.Code != int(fault.ErrCanceled) {
			return true
		}
	}
	return false
}
