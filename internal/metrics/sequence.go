// Package metrics analyses received RTP for sequence anomalies and jitter.
package metrics

// MinJitterSamples is the number of interarrival samples required before a
// jitter summary is considered usable.
const MinJitterSamples = 10

// JitterStatusOK marks a summary built from enough samples.
const JitterStatusOK = "ok"

// JitterStatusInsufficient marks a summary whose sample count is too low.
const JitterStatusInsufficient = "insufficient_samples"

// SequenceReport is the completed sequence analysis.
type SequenceReport struct {
	Received     int
	ExpectedSpan int
	Lost         int
	Duplicates   int
	Reordered    int
	FirstSeq     uint16
	LastSeq      uint16
	FirstExt     int64
	LastExt      int64
}

// SequenceAnalyzer tracks 16-bit sequence numbers across wrap-around.
type SequenceAnalyzer struct {
	seen        map[int64]int
	initialized bool
	firstExt    int64
	highExt     int64
	lastExt     int64
	received    int
	duplicates  int
	reordered   int
}

// NewSequenceAnalyzer returns an empty analyzer.
func NewSequenceAnalyzer() *SequenceAnalyzer {
	return &SequenceAnalyzer{seen: make(map[int64]int)}
}

// Add ingests one packet's sequence number.
func (a *SequenceAnalyzer) Add(seq uint16) {
	if !a.initialized {
		a.initialized = true
		ext := int64(seq)
		a.firstExt = ext
		a.highExt = ext
		a.lastExt = ext
		a.seen[ext]++
		a.received++
		return
	}

	// Unwrap relative to the highest extended sequence seen so far.
	ext := a.highExt + int64(int16(seq-uint16(a.highExt))) //nolint:gosec // deliberate 16-bit wrap
	switch {
	case ext > a.highExt:
		a.highExt = ext
	case a.seen[ext] > 0:
		a.duplicates++
	default:
		a.reordered++
	}
	a.lastExt = ext
	a.seen[ext]++
	a.received++
}

// SeenExtensions returns a copy of the extended sequence numbers observed and
// their packet counts.
func (a *SequenceAnalyzer) SeenExtensions() map[int64]int {
	out := make(map[int64]int, len(a.seen))
	for k, v := range a.seen {
		out[k] = v
	}
	return out
}

// Report finalizes the analysis. Lost counts gaps in the contiguous span and
// therefore includes packets dropped before the receiver saw them; packets
// that merely arrived late are not lost.
func (a *SequenceAnalyzer) Report() SequenceReport {
	if !a.initialized {
		return SequenceReport{}
	}
	span := int(a.highExt-a.firstExt) + 1
	unique := len(a.seen)
	lost := span - unique
	if lost < 0 {
		lost = 0
	}
	return SequenceReport{
		Received:     a.received,
		ExpectedSpan: span,
		Lost:         lost,
		Duplicates:   a.duplicates,
		Reordered:    a.reordered,
		FirstSeq:     uint16(a.firstExt),
		LastSeq:      uint16(a.lastExt),
		FirstExt:     a.firstExt,
		LastExt:      a.lastExt,
	}
}

// TimestampReport captures timestamp continuity across 32-bit wrap.
type TimestampReport struct {
	Received       int
	WrapCount      int
	Regressions    int
	FirstTimestamp uint32
	LastTimestamp  uint32
	TotalSamples   int64
}

// TimestampAnalyzer checks RTP timestamps with modular 32-bit arithmetic.
type TimestampAnalyzer struct {
	initialized bool
	lastTS      uint32
	lastExt     int64
	firstTS     uint32
	received    int
	wraps       int
	regressions int
}

// NewTimestampAnalyzer returns an empty analyzer.
func NewTimestampAnalyzer() *TimestampAnalyzer { return &TimestampAnalyzer{} }

// Add ingests one packet timestamp. Regressions are counted when the modular
// delta is non-positive; wraps are counted when the unsigned value decreases
// while the modular delta remains positive.
func (t *TimestampAnalyzer) Add(ts uint32) {
	if !t.initialized {
		t.initialized = true
		t.lastTS = ts
		t.firstTS = ts
		t.received = 1
		return
	}
	delta := int64(int32(ts - t.lastTS))
	t.received++
	if delta <= 0 {
		t.regressions++
	} else {
		if ts < t.lastTS {
			t.wraps++
		}
		t.lastExt += delta
		t.lastTS = ts
	}
}

// Report finalizes the timestamp analysis.
func (t *TimestampAnalyzer) Report() TimestampReport {
	if !t.initialized {
		return TimestampReport{}
	}
	return TimestampReport{
		Received:       t.received,
		WrapCount:      t.wraps,
		Regressions:    t.regressions,
		FirstTimestamp: t.firstTS,
		LastTimestamp:  t.lastTS,
		TotalSamples:   t.lastExt,
	}
}
