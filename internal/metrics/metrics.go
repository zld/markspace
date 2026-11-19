// Package metrics aggregates playout counters, watermark samples and
// target-delay quantiles. All ratios are guarded against division by zero
// and report "no data" explicitly.
package metrics

import "sort"

// Watermark is one buffer-level sample taken at a fixed playout interval.
type Watermark struct {
	TimeMs         float64 `json:"t_ms"`
	StreamID       uint32  `json:"stream"`
	BufferedFrames int     `json:"buffered_frames"`
	TargetDelayMs  float64 `json:"target_delay_ms"`
}

// Metrics accumulates counters for one run. Not safe for concurrent use;
// receivers drive it from a single goroutine.
type Metrics struct {
	Received         uint64
	DuplicateDropped uint64
	LateDropped      uint64
	Malformed        uint64
	Played           uint64
	Concealed        uint64
	Unrecoverable    uint64
	MaxConcealRun    int

	concealRun int
	Watermarks []Watermark
	targets    []float64
}

// RecordPlayed accounts one real frame emitted to the decoder.
func (m *Metrics) RecordPlayed() {
	m.Played++
	m.concealRun = 0
}

// RecordConceal accounts one concealed frame and tracks run length.
func (m *Metrics) RecordConceal(unrecoverable bool) {
	m.Concealed++
	m.concealRun++
	if m.concealRun > m.MaxConcealRun {
		m.MaxConcealRun = m.concealRun
	}
	if unrecoverable {
		m.Unrecoverable++
	}
}

// Sample appends a watermark and its target-delay observation.
func (m *Metrics) Sample(w Watermark) {
	m.Watermarks = append(m.Watermarks, w)
	m.targets = append(m.targets, w.TargetDelayMs)
}

// Continuity returns played/(played+concealed); ok is false when no frame
// was ever output, so callers can emit an explicit null.
func (m *Metrics) Continuity() (ratio float64, ok bool) {
	total := m.Played + m.Concealed
	if total == 0 {
		return 0, false
	}
	return float64(m.Played) / float64(total), true
}

// TargetQuantile returns the q-quantile (0..1) of observed target delays;
// ok is false when no watermark was ever sampled.
func (m *Metrics) TargetQuantile(q float64) (v float64, ok bool) {
	if len(m.targets) == 0 {
		return 0, false
	}
	s := append([]float64(nil), m.targets...)
	sort.Float64s(s)
	idx := int(q*float64(len(s)-1) + 0.5)
	return s[idx], true
}
