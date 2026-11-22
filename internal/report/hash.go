// Package report produces per-segment hashes and the deterministic
// run report that separates successful segments from quarantined
// records. All numbers derive solely from file content and plans, so
// repeated runs over the same inputs yield identical hashes.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"mediq/internal/bitstream"
	"mediq/internal/index"
	"mediq/internal/segment"
	"mediq/internal/timeline"
)

// SegmentHash is the checksum of one materialized segment file.
type SegmentHash struct {
	RepID    string `json:"representation"`
	Boundary int    `json:"boundary"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// InjectionCounts records the damage types injected or detected.
type InjectionCounts struct {
	TruncatedStreams int `json:"truncated_streams"`
	CorruptUnits     int `json:"corrupt_units"`
	MissingIndex     int `json:"missing_index_rows"`
	DuplicateIndex   int `json:"duplicate_index_rows"`
	MisalignedReps   int `json:"misaligned_representations"`
}

// Report is the machine-readable run summary.
type Report struct {
	SchemaVersion     int                `json:"schema_version"`
	GeneratedAt       string             `json:"generated_at_utc"`
	Segments          int                `json:"segments"`
	Quarantined       int                `json:"quarantined_segments"`
	KeyframeIndexHit  float64            `json:"keyframe_index_hit_rate"`
	RoundtripPct      float64            `json:"roundtrip_consistency"`
	SHA256MatchPct    float64            `json:"segment_sha256_match"`
	CrossRepSkewMs    float64            `json:"cross_representation_skew_ms"`
	Replayed          int                `json:"wal_replayed"`
	RolledBack        int                `json:"rolled_back"`
	Injections        InjectionCounts    `json:"injected_faults"`
	Hashes            []SegmentHash      `json:"segment_hashes"`
	QuarantineRecords []QuarantineRecord `json:"quarantine"`
	Errors            []*timeline.Error  `json:"errors"`
}

// QuarantineRecord describes one isolated, uncommittable item.
type QuarantineRecord struct {
	Kind   string `json:"kind"`
	RepID  string `json:"representation,omitempty"`
	Offset int64  `json:"offset"`
	Detail string `json:"detail"`
	Action string `json:"recovery_action"`
}

// Builder accumulates evidence during a pipeline run.
type Builder struct {
	hashes     []SegmentHash
	quarantine []QuarantineRecord
	errs       []*timeline.Error
}

// NewBuilder creates a report builder.
func NewBuilder() *Builder { return &Builder{} }

// HashSegment reads a materialized segment file and records its hash.
func (b *Builder) HashSegment(repID string, boundary int, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	b.hashes = append(b.hashes, SegmentHash{
		RepID: repID, Boundary: boundary, Path: path, Size: n,
		SHA256: hex.EncodeToString(h.Sum(nil)),
	})
	return nil
}

// AddError records a shared-model error in the report.
func (b *Builder) AddError(e *timeline.Error) {
	if e != nil {
		b.errs = append(b.errs, e)
	}
}

// QuarantineIndex moves reconcile findings that failed validation into
// the quarantine section of the report.
func (b *Builder) QuarantineIndex(repID string, rec *index.Result) {
	for _, f := range rec.Quarantine {
		b.quarantine = append(b.quarantine, QuarantineRecord{
			Kind:   string(f.Status),
			RepID:  repID,
			Offset: f.Entry.Offset,
			Detail: fmt.Sprintf("seq=%d pts=%d", f.Entry.Seq, int64(f.Entry.PTS)),
			Action: "isolate_index_entry",
		})
	}
}

// QuarantineSegments records quarantined segments from a plan.
func (b *Builder) QuarantineSegments(p *segment.Plan) {
	for _, s := range p.Segments {
		if !s.Quarantined {
			continue
		}
		b.quarantine = append(b.quarantine, QuarantineRecord{
			Kind:   "corrupt_unit",
			RepID:  p.RepID,
			Offset: s.StartOffset,
			Detail: fmt.Sprintf("boundary=%d seq=%d", s.Index, s.KeyframeSeq),
			Action: "isolate_segment",
		})
	}
}

// Finalize freezes the report from all collected evidence.
// shaMatched/shaTotal describe the re-hash verification performed by
// the caller (e.g. manifest verify or a post-recovery verify run).
func (b *Builder) Finalize(plans []*segment.Plan, hitRate, roundtrip, skew float64,
	shaMatched, shaTotal, replayed, rolledBack int, inj InjectionCounts, at time.Time) *Report {
	sort.SliceStable(b.hashes, func(i, j int) bool {
		if b.hashes[i].RepID != b.hashes[j].RepID {
			return b.hashes[i].RepID < b.hashes[j].RepID
		}
		return b.hashes[i].Boundary < b.hashes[j].Boundary
	})
	segments, quarantined := 0, 0
	for _, p := range plans {
		for _, s := range p.Segments {
			segments++
			if s.Quarantined {
				quarantined++
			}
		}
	}
	shaPct := 100.0
	if shaTotal > 0 {
		shaPct = float64(shaMatched) * 100 / float64(shaTotal)
	}
	errs := append([]*timeline.Error(nil), b.errs...)
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Stage != errs[j].Stage {
			return errs[i].Stage < errs[j].Stage
		}
		return errs[i].Offset < errs[j].Offset
	})
	return &Report{
		SchemaVersion:     1,
		GeneratedAt:       at.UTC().Format(time.RFC3339),
		Segments:          segments,
		Quarantined:       quarantined,
		KeyframeIndexHit:  hitRate,
		RoundtripPct:      roundtrip,
		SHA256MatchPct:    shaPct,
		CrossRepSkewMs:    skew,
		Replayed:          replayed,
		RolledBack:        rolledBack,
		Injections:        inj,
		Hashes:            b.hashes,
		QuarantineRecords: b.quarantine,
		Errors:            errs,
	}
}

// Marshal renders the report deterministically.
func Marshal(r *Report) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// CodecSummary is used by demo output for operator-facing summaries.
type CodecSummary struct {
	Codec     bitstream.Codec
	Units     int
	Keyframes int
}
