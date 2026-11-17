// Package metrics aggregates receive-side statistics and renders the final
// report (text or CSV) including the bucketed queue-depth curve.
package metrics

import (
	"fmt"
	"io"
	"sort"
)

// Stats collects all counters for one run. Not safe for concurrent use;
// the demo drivers are single-goroutine.
type Stats struct {
	Input         int64 // valid packets accepted from the wire/trace
	NetworkLost   int64 // frames missing at their playout deadline
	DuplicateDrop int64
	ConflictDrop  int64
	LateDrop      int64 // arrived after their playout deadline
	OverflowDrop  int64 // dropped because the queue was full
	BadPackets    int64 // truncated/oversized/bad-checksum datagrams

	Played    int64 // real frames emitted
	PLCFrames int64 // concealed frames emitted

	MaxPLCRun int64
	curRun    int64

	Jumps []int // resume boundary jumps (last concealed vs first real sample)

	Timeouts  int64
	EndReason string

	bucketMs int64
	buckets  []bucket
}

type bucket struct {
	sum int64
	n   int64
}

// New returns a Stats that buckets queue-depth observations every bucketMs
// milliseconds (clamped to >= 10).
func New(bucketMs int64) *Stats {
	if bucketMs < 10 {
		bucketMs = 10
	}
	return &Stats{bucketMs: bucketMs}
}

// NoteReal records one emitted real frame.
func (s *Stats) NoteReal() {
	s.Played++
	s.curRun = 0
}

// NotePLC records one emitted concealment frame.
func (s *Stats) NotePLC() {
	s.PLCFrames++
	s.curRun++
	if s.curRun > s.MaxPLCRun {
		s.MaxPLCRun = s.curRun
	}
}

// NoteJump records a resume boundary jump.
func (s *Stats) NoteJump(j int) { s.Jumps = append(s.Jumps, j) }

// Observe samples the queue depth at time nowMs.
func (s *Stats) Observe(nowMs int64, depth int) {
	idx := nowMs / s.bucketMs
	for int64(len(s.buckets)) <= idx {
		s.buckets = append(s.buckets, bucket{})
	}
	s.buckets[idx].sum += int64(depth)
	s.buckets[idx].n++
}

// DropTotal is the total number of frames lost to the network or dropped
// by the receiver (duplicates excluded: no media was lost).
func (s *Stats) DropTotal() int64 {
	return s.NetworkLost + s.LateDrop + s.OverflowDrop
}

// PLCRatio is the fraction of emitted frames that were concealed.
func (s *Stats) PLCRatio() float64 {
	total := s.Played + s.PLCFrames
	if total == 0 {
		return 0
	}
	return float64(s.PLCFrames) / float64(total)
}

// JumpP95 is the 95th percentile resume boundary jump (0 if none).
func (s *Stats) JumpP95() int {
	if len(s.Jumps) == 0 {
		return 0
	}
	sorted := make([]int, len(s.Jumps))
	copy(sorted, s.Jumps)
	sort.Ints(sorted)
	idx := (95*len(sorted)+99)/100 - 1
	return sorted[idx]
}

// CurveRow is one bucket of the queue-depth curve.
type CurveRow struct {
	TimeMs      int64
	QueueFrames float64
}

// Curve returns the bucketed queue-depth curve.
func (s *Stats) Curve() []CurveRow {
	rows := make([]CurveRow, 0, len(s.buckets))
	for i, b := range s.buckets {
		if b.n == 0 {
			continue
		}
		rows = append(rows, CurveRow{
			TimeMs:      int64(i) * s.bucketMs,
			QueueFrames: float64(b.sum) / float64(b.n),
		})
	}
	return rows
}

// WriteText renders the report as key=value lines plus the curve.
func (s *Stats) WriteText(w io.Writer) error {
	if err := s.writeMetrics(w, "%s=%v\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "curve_bucket_ms=%d\n", s.bucketMs); err != nil {
		return err
	}
	return s.writeCurve(w)
}

// WriteCSV renders the report as key,value lines plus the curve.
func (s *Stats) WriteCSV(w io.Writer) error {
	if err := s.writeMetrics(w, "%s,%v\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "curve_bucket_ms,%d\n", s.bucketMs); err != nil {
		return err
	}
	return s.writeCurve(w)
}

func (s *Stats) writeMetrics(w io.Writer, format string) error {
	rows := []struct {
		k string
		v any
	}{
		{"input", s.Input},
		{"network_lost", s.NetworkLost},
		{"duplicate_drop", s.DuplicateDrop},
		{"conflict_drop", s.ConflictDrop},
		{"late_drop", s.LateDrop},
		{"overflow_drop", s.OverflowDrop},
		{"bad_packets", s.BadPackets},
		{"drop_total", s.DropTotal()},
		{"played_frames", s.Played},
		{"plc_frames", s.PLCFrames},
		{"plc_ratio", fmt.Sprintf("%.4f", s.PLCRatio())},
		{"max_plc_run", s.MaxPLCRun},
		{"resume_jump_p95", s.JumpP95()},
		{"timeouts", s.Timeouts},
		{"end_reason", s.EndReason},
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(w, format, r.k, r.v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stats) writeCurve(w io.Writer) error {
	if _, err := fmt.Fprintln(w, "time_ms,queue_frames"); err != nil {
		return err
	}
	for _, r := range s.Curve() {
		if _, err := fmt.Fprintf(w, "%d,%.2f\n", r.TimeMs, r.QueueFrames); err != nil {
			return err
		}
	}
	return nil
}
