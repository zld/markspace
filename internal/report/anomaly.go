// Package report renders the traceable outputs of a run: an anomaly listing,
// per-segment hashes and the recovery boundaries. Every rendering is stable
// for a given input so two runs can be compared byte for byte.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"

	"medtrace/internal/core"
	"medtrace/internal/stream"
)

// Report is the complete output document. It is JSON-serialisable and also
// rendered to text by Write; the JSON form is what the hash covers.
type Report struct {
	// Interval is the requested half-open range.
	FromNS int64 `json:"from_ns"`
	ToNS   int64 `json:"to_ns"`
	// Anomalies are sorted by canonical time, segment and offset.
	Anomalies []AnomalyRecord `json:"anomalies"`
	// Hashes are per-segment SHA-256 values in manifest order.
	Hashes []HashRecord `json:"hashes"`
	// Recoveries in manifest order.
	Recoveries []RecoveryRecord `json:"recoveries"`
	// Epochs are the canonical timeline boundaries assigned during scanning.
	Epochs []EpochRecord `json:"epochs"`
	// Stats are the summary counters the verify command asserts on.
	Stats Stats `json:"stats"`
}

// AnomalyRecord is the stable, serialisable form of core.Anomaly.
type AnomalyRecord struct {
	Category       string `json:"category"`
	Reason         string `json:"reason,omitempty"`
	File           string `json:"file"`
	Line           int    `json:"line,omitempty"`
	Segment        string `json:"segment"`
	ByteOffset     int64  `json:"byte_offset"`
	Track          string `json:"track"`
	RawTimestamp   int64  `json:"raw_timestamp"`
	Timescale      uint32 `json:"timescale"`
	CanonicalNS    int64  `json:"canonical_ns"`
	BoundaryOffset int64  `json:"boundary_offset_ns,omitempty"`
	Expected       string `json:"expected,omitempty"`
	Actual         string `json:"actual,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

// HashRecord is one segment's digest and canonical bounds.
type HashRecord struct {
	URI     string `json:"uri"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	StartNS int64  `json:"start_ns"`
	EndNS   int64  `json:"end_ns"`
	Epoch   int    `json:"epoch"`
}

// RecoveryRecord is the stable form of a local recovery.
type RecoveryRecord struct {
	FailedSegment     string `json:"failed_segment"`
	FailedByteOffset  int64  `json:"failed_byte_offset"`
	UnrecoveredFromNS int64  `json:"unrecovered_from_ns"`
	UnrecoveredToNS   int64  `json:"unrecovered_to_ns"`
	ResumeSegment     string `json:"resume_segment,omitempty"`
	ResumeByteOffset  int64  `json:"resume_byte_offset"`
	ResumeCanonicalNS int64  `json:"resume_canonical_ns"`
	Resumed           bool   `json:"resumed"`
}

// EpochRecord is one canonical timeline boundary.
type EpochRecord struct {
	Number         int    `json:"number"`
	Reason         string `json:"reason"`
	Source         string `json:"source"`
	RawBase        int64  `json:"raw_base"`
	CanonicalNS    int64  `json:"canonical_ns"`
	BoundaryOffset int64  `json:"boundary_offset_ns"`
}

// Stats are the counters printed at the end of a run and asserted by verify.
type Stats struct {
	Roundtrip        int `json:"roundtrip"`
	RoundtripTotal   int `json:"roundtrip_total"`
	KeyframeHit      int `json:"keyframe_hit"`
	KeyframeTotal    int `json:"keyframe_total"`
	SegmentHashes    int `json:"segment_hashes"`
	SegmentTotal     int `json:"segment_total"`
	HashMismatch     int `json:"hash_mismatch"`
	Anomalies        int `json:"anomalies"`
	Recovered        int `json:"recovered"`
	OffsetPreserved  int `json:"offset_preserved"`
	CacheInvalidated int `json:"cache_invalidated"`
}

// Build assembles a Report from the scan and run results.
func Build(from, to core.Nanos, idx *core.Index, epochs []core.Epoch, scanAnomalies []core.Anomaly, res *stream.Result) *Report {
	r := &Report{FromNS: int64(from), ToNS: int64(to)}
	all := append(append([]core.Anomaly{}, scanAnomalies...), res.Anomalies...)
	core.SortAnomalies(all)
	for _, a := range all {
		r.Anomalies = append(r.Anomalies, AnomalyRecord{
			Category: string(a.Category), Reason: a.Reason, File: a.Pos.File, Line: a.Pos.Line,
			Segment: a.Segment, ByteOffset: a.ByteOffset, Track: a.Track.Key(),
			RawTimestamp: a.RawTimestamp, Timescale: a.Timescale, CanonicalNS: int64(a.Canonical),
			BoundaryOffset: int64(a.BoundaryOffset), Expected: a.Expected, Actual: a.Actual, Detail: a.Detail,
		})
	}
	for _, s := range idx.Segments {
		r.Hashes = append(r.Hashes, HashRecord{
			URI: s.URI, SHA256: s.SHA256, Size: s.Size,
			StartNS: int64(s.Start), EndNS: int64(s.End), Epoch: s.Epoch,
		})
	}
	for _, rec := range res.Recoveries {
		r.Recoveries = append(r.Recoveries, RecoveryRecord{
			FailedSegment: rec.FailedSegment, FailedByteOffset: rec.FailedByteOffset,
			UnrecoveredFromNS: int64(rec.UnrecoveredStart), UnrecoveredToNS: int64(rec.UnrecoveredEnd),
			ResumeSegment: rec.ResumeSegment, ResumeByteOffset: rec.ResumeByteOffset,
			ResumeCanonicalNS: int64(rec.ResumeCanonical), Resumed: rec.Resumed,
		})
	}
	for _, e := range epochs {
		r.Epochs = append(r.Epochs, EpochRecord{
			Number: e.Number, Reason: string(e.Reason), Source: e.Source, RawBase: e.RawBase,
			CanonicalNS: int64(e.CanonicalBase), BoundaryOffset: int64(e.BoundaryOffset),
		})
	}
	// Segment hashes counts the fragments whose digest was verified this run;
	// a mismatch is reported separately in HashMismatch, so the ratio reads
	// "verified clean / verified".
	r.Stats = Stats{
		SegmentHashes: res.HashChecks,
		SegmentTotal:  res.HashChecks,
		KeyframeHit:   res.KeyframeHits,
		KeyframeTotal: res.KeyframeTries,
		Anomalies:     len(r.Anomalies),
	}
	return r
}

// recount recomputes the content-derived counters (anomalies, hash
// mismatches, recovered, offset preserved) from the report's own lists.
func (r *Report) recount() {
	r.Stats.Anomalies = len(r.Anomalies)
	r.Stats.HashMismatch = 0
	for _, a := range r.Anomalies {
		if a.Category == string(core.CatHashMismatch) {
			r.Stats.HashMismatch++
		}
	}
	r.Stats.Recovered = 0
	r.Stats.OffsetPreserved = 0
	for _, rec := range r.Recoveries {
		if !rec.Resumed {
			continue
		}
		r.Stats.Recovered++
		if rec.ResumeByteOffset >= 0 && rec.ResumeByteOffset != rec.FailedByteOffset {
			r.Stats.OffsetPreserved++
		}
	}
}

// Merge folds other into r, concatenating anomalies, hashes, recoveries and
// epochs and re-sorting, so a run over two inputs produces one document. The
// interval of the combined report spans both.
func (r *Report) Merge(other *Report) {
	if other == nil {
		return
	}
	r.Anomalies = append(r.Anomalies, other.Anomalies...)
	sort.SliceStable(r.Anomalies, func(i, j int) bool { return anomalyLess(r.Anomalies[i], r.Anomalies[j]) })
	r.Hashes = append(r.Hashes, other.Hashes...)
	r.Recoveries = append(r.Recoveries, other.Recoveries...)
	// Epoch numbers restart per input, so renumber after concatenation.
	base := len(r.Epochs)
	for _, e := range other.Epochs {
		e.Number = base
		base++
		r.Epochs = append(r.Epochs, e)
	}
	if other.FromNS < r.FromNS {
		r.FromNS = other.FromNS
	}
	if other.ToNS > r.ToNS {
		r.ToNS = other.ToNS
	}
	if other.Stats.KeyframeHit > r.Stats.KeyframeHit {
		r.Stats.KeyframeHit = other.Stats.KeyframeHit
	}
	r.Stats.SegmentHashes += other.Stats.SegmentHashes
	r.Stats.SegmentTotal += other.Stats.SegmentTotal
	r.Stats.KeyframeTotal += other.Stats.KeyframeTotal
	r.recount()
}

// anomalyLess mirrors core.Anomaly.Less on the record form.
func anomalyLess(a, b AnomalyRecord) bool {
	if a.CanonicalNS != b.CanonicalNS {
		return a.CanonicalNS < b.CanonicalNS
	}
	if a.Segment != b.Segment {
		return a.Segment < b.Segment
	}
	if a.ByteOffset != b.ByteOffset {
		return a.ByteOffset < b.ByteOffset
	}
	if a.Category != b.Category {
		return a.Category < b.Category
	}
	return a.Reason < b.Reason
}

// Digest returns the SHA-256 of the report's JSON encoding. Two runs that
// produce the same digest produced the same report.
func (r *Report) Digest() (string, error) {
	b, err := marshalStable(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Write renders the human-readable terminal form.
func (r *Report) Write(w io.Writer) error {
	bw := &errWriter{w: w}
	bw.printf("interval: [%s, %s)\n", core.Nanos(r.FromNS), core.Nanos(r.ToNS))
	bw.printf("\nanomalies (%d):\n", len(r.Anomalies))
	for _, a := range r.Anomalies {
		bw.printf("  %-22s %-22s %s@%d %s", a.Category, a.Reason, a.Segment, a.ByteOffset, a.Track)
		if a.Expected != "" {
			bw.printf(" expected=%s actual=%s", short(a.Expected), short(a.Actual))
		}
		if a.Detail != "" {
			bw.printf(" (%s)", a.Detail)
		}
		bw.printf("\n")
	}
	bw.printf("\nsegment hashes (%d):\n", len(r.Hashes))
	for _, h := range r.Hashes {
		bw.printf("  %s  %s  %d bytes  [%s,%s) epoch=%d\n",
			short(h.SHA256), h.URI, h.Size, core.Nanos(h.StartNS), core.Nanos(h.EndNS), h.Epoch)
	}
	bw.printf("\nepochs (%d):\n", len(r.Epochs))
	for _, e := range r.Epochs {
		bw.printf("  #%d %-22s %-28s raw=%d canonical=%s boundary=%s\n",
			e.Number, e.Reason, e.Source, e.RawBase, core.Nanos(e.CanonicalNS), core.Nanos(e.BoundaryOffset))
	}
	bw.printf("\nrecoveries (%d):\n", len(r.Recoveries))
	for _, rec := range r.Recoveries {
		bw.printf("  failed=%s@%d unrecovered=[%s,%s) resumed=%v",
			rec.FailedSegment, rec.FailedByteOffset,
			core.Nanos(rec.UnrecoveredFromNS), core.Nanos(rec.UnrecoveredToNS), rec.Resumed)
		if rec.Resumed {
			bw.printf(" resume=%s@%d (%s)", rec.ResumeSegment, rec.ResumeByteOffset, core.Nanos(rec.ResumeCanonicalNS))
		}
		bw.printf("\n")
	}
	bw.printf("\nstats: roundtrip=%d/%d keyframe_hit=%d/%d segment_hashes=%d/%d hash_mismatch=%d anomalies=%d recovered=%d offset_preserved=%d cache_invalidated=%d\n",
		r.Stats.Roundtrip, r.Stats.RoundtripTotal, r.Stats.KeyframeHit, r.Stats.KeyframeTotal,
		r.Stats.SegmentHashes, r.Stats.SegmentTotal, r.Stats.HashMismatch, r.Stats.Anomalies,
		r.Stats.Recovered, r.Stats.OffsetPreserved, r.Stats.CacheInvalidated)
	digest, err := r.Digest()
	if err != nil {
		return err
	}
	bw.printf("report_sha256=%s\n", digest)
	return bw.err
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}

// sortedUnique returns keys sorted, used by callers that list anomaly
// categories.
func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

var _ = sortedUnique
var _ = strings.TrimSpace

// Recount refreshes the content-derived counters from the report's lists.
func (r *Report) Recount() { r.recount() }
