// Package report renders deterministic, auditable processing results to the
// terminal and to a machine-readable JSON document.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"mediamerge/internal/merge"
	"mediamerge/internal/track"
	"mediamerge/internal/txn"
)

// Metrics holds every counter and hash surfaced by a verify run.
type Metrics struct {
	Case            string         `json:"case"`
	RoundtripOK     int            `json:"roundtrip_ok"`
	RoundtripTotal  int            `json:"roundtrip_total"`
	KeyframeHits    int            `json:"keyframe_hits"`
	KeyframeTotal   int            `json:"keyframe_total"`
	Segments        int            `json:"segments"`
	HashMismatch    int            `json:"hash_mismatch"`
	Conflicts       int            `json:"conflicts"`
	Rollbacks       int            `json:"rollbacks"`
	Gaps            int            `json:"gaps"`
	Rejected        int            `json:"rejected"`
	DuplicateCues   int            `json:"duplicate_cues"`
	Replayed        int            `json:"replayed"`
	HashMatchOK     int            `json:"hash_match_ok,omitempty"`
	HashMatchTotal  int            `json:"hash_match_total,omitempty"`
	Interrupted     bool           `json:"interrupted,omitempty"`
	// ResumeReport forces replayed/duplicate_cues lines even when zero, which
	// the recovery acceptance output requires explicitly.
	ResumeReport    bool           `json:"resume_report,omitempty"`
	ManifestChanged bool           `json:"manifest_changed"`
	SegmentHashes   []SegmentHash  `json:"segment_hashes"`
	GapRecords      []track.Gap    `json:"gap_records,omitempty"`
	RejectRecords   []track.Rejected `json:"reject_records,omitempty"`
	ConflictRecords []merge.Conflict `json:"conflict_records,omitempty"`
	RollbackReason  string         `json:"rollback_reason,omitempty"`
}

// SegmentHash pairs a segment id with its 64-hex-char SHA-256.
type SegmentHash struct {
	ID     int    `json:"id"`
	SHA256 string `json:"sha256"`
}

// FromMerge fills merge-derived counters and records.
func (m *Metrics) FromMerge(res *merge.Result) {
	if res == nil {
		return
	}
	m.Conflicts = len(res.Conflicts)
	m.ConflictRecords = res.Conflicts
	m.Gaps = len(res.Gaps)
	m.GapRecords = res.Gaps
	m.Rejected = len(res.Rejected)
	m.RejectRecords = res.Rejected
	m.DuplicateCues = res.Duplicates
	total := 0
	for _, tl := range res.Timelines {
		total += len(tl.Cues)
	}
	m.RoundtripOK, m.RoundtripTotal = total, total
}

// FromManifest fills segment and hash counters from a committed manifest.
func (m *Metrics) FromManifest(man *txn.Manifest, mismatches int) {
	if man == nil {
		return
	}
	m.Segments = len(man.Segments)
	m.HashMismatch = mismatches
	m.SegmentHashes = make([]SegmentHash, 0, len(man.Segments))
	for _, s := range man.Segments {
		m.SegmentHashes = append(m.SegmentHashes, SegmentHash{ID: s.ID, SHA256: s.SHA256})
	}
	sort.SliceStable(m.SegmentHashes, func(i, j int) bool {
		return m.SegmentHashes[i].ID < m.SegmentHashes[j].ID
	})
}

// Keyframe sets the keyframe hit counters.
func (m *Metrics) Keyframe(hits, total int) {
	m.KeyframeHits, m.KeyframeTotal = hits, total
}

// HashMatch sets the post-resume per-segment hash verification counters.
func (m *Metrics) HashMatch(ok, total int) {
	m.HashMatchOK, m.HashMatchTotal = ok, total
}

// MarkInterrupted records a simulated interrupt for audit output.
func (m *Metrics) MarkInterrupted() { m.Interrupted = true }

// WriteJSON emits the full machine-readable report.
func (m *Metrics) WriteJSON(w io.Writer) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// WriteText emits the stable, greppable terminal summary plus one line per
// segment showing its 64-character SHA-256 digest.
func (m *Metrics) WriteText(w io.Writer) {
	fmt.Fprintf(w, "roundtrip=%d/%d\n", m.RoundtripOK, m.RoundtripTotal)
	fmt.Fprintf(w, "keyframe_hit=%d/%d\n", m.KeyframeHits, m.KeyframeTotal)
	fmt.Fprintf(w, "segments=%d\n", m.Segments)
	fmt.Fprintf(w, "hash_mismatch=%d\n", m.HashMismatch)
	if m.Conflicts != 0 {
		fmt.Fprintf(w, "conflicts=%d\n", m.Conflicts)
	}
	if m.Rollbacks != 0 {
		fmt.Fprintf(w, "rollback=%d\n", m.Rollbacks)
	}
	if m.Gaps != 0 {
		fmt.Fprintf(w, "gap=%d\n", m.Gaps)
	}
	if m.Rejected != 0 {
		fmt.Fprintf(w, "rejected=%d\n", m.Rejected)
	}
	if m.DuplicateCues != 0 || m.ResumeReport {
		fmt.Fprintf(w, "duplicate_cues=%d\n", m.DuplicateCues)
	}
	if m.Replayed != 0 || m.ResumeReport {
		fmt.Fprintf(w, "replayed=%d\n", m.Replayed)
	}
	if m.HashMatchTotal != 0 {
		fmt.Fprintf(w, "hash_match=%d/%d\n", m.HashMatchOK, m.HashMatchTotal)
	}
	if m.Interrupted {
		fmt.Fprintln(w, "interrupted=1")
	}
	if !m.ManifestChanged {
		fmt.Fprintln(w, "manifest_unchanged=1")
	}
	for _, sh := range m.SegmentHashes {
		fmt.Fprintf(w, "segment[%d] sha256=%s\n", sh.ID, sh.SHA256)
	}
	if m.RollbackReason != "" {
		fmt.Fprintf(w, "rollback_reason=%s\n", m.RollbackReason)
	}
}
