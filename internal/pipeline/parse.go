// Package pipeline chains the parse, transform and validate stages over
// manifest-mapped media segments. Localized damage isolates only the
// affected region and its dependent index entries; verified prefixes and
// independent later segments keep processing.
package pipeline

import (
	"encoding/json"

	"mediascan/internal/fault"
	"mediascan/internal/index"
	"mediascan/internal/report"
	"mediascan/internal/stream"
)

// SegmentInput pairs manifest metadata with the raw segment bytes.
type SegmentInput struct {
	Name     string
	MediaSeq uint64
	// Present is false when the manifest references a segment whose bytes
	// were never delivered (sequence gap).
	Present bool
	Data    []byte
	// ManifestOffset points back into the source playlist/MPD.
	ManifestOffset int64
	// DurationSeconds is the manifest-declared span, used as the
	// reference-clock fallback when sample timing cannot be parsed.
	DurationSeconds float64
}

// ParsedSegment is the parse-stage output for one input.
type ParsedSegment struct {
	Input     SegmentInput
	MP4       *stream.MP4Result
	InputHash string
	ParseErr  error
	Damaged   bool
}

// NormalizedSample is one sample on the unified rational second base.
type NormalizedSample struct {
	Index       int     `json:"index"`
	TrackID     uint32  `json:"track_id"`
	DTS         uint64  `json:"dts_units"`
	Duration    uint32  `json:"duration_units"`
	Keyframe    bool    `json:"keyframe"`
	ByteOffset  int64   `json:"byte_offset"`
	Size        uint32  `json:"size"`
	TimeSeconds float64 `json:"time_seconds"`
	// RawSeconds is the uncorrected mapping; CorrectionDelta is the
	// drift adjustment applied (corrected = raw + delta).
	TimeSecondsRaw  float64 `json:"time_seconds_raw"`
	CorrectionDelta float64 `json:"correction_delta_seconds"`
}

// NormalizedSegment is the transform-stage artifact. Its canonical JSON
// form is what gets hashed and re-parsed during validation.
type NormalizedSegment struct {
	Segment      string             `json:"segment"`
	MediaSeq     uint64             `json:"media_seq"`
	StartSeconds float64            `json:"start_seconds"`
	Timescale    uint32             `json:"timescale"`
	MediaSeqBox  uint32             `json:"media_seq_box"`
	BaseDTS      uint64             `json:"base_dts"`
	DriftPPM     float64            `json:"drift_ppm"`
	Boxes        []stream.BoxInfo   `json:"boxes"`
	Samples      []NormalizedSample `json:"samples"`
	SafePrefix   int64              `json:"safe_prefix"`
	Damaged      bool               `json:"damaged"`
	Quarantined  int                `json:"quarantined_entries"`
}

// Result is the full per-segment pipeline outcome.
type Result struct {
	Parsed     *ParsedSegment
	Normalized *NormalizedSegment
	OutputHash string
	// RoundtripOK is false when the validate stage found a divergence.
	RoundtripOK bool
}

// Pipeline holds shared configuration and accumulators.
type Pipeline struct {
	DriftPPM     float64
	DefaultTimes uint32
	Log          *fault.Log
	Index        *index.Index
	Hasher       *report.Hasher
	results      []*Result
	// knownMissing holds media sequence numbers already reported as gaps
	// by the manifest layer, so the parse stage does not double-report.
	knownMissing map[uint64]bool
}

// New constructs a pipeline with empty accumulators.
func New(driftPPM float64) *Pipeline {
	return &Pipeline{
		DriftPPM:     driftPPM,
		DefaultTimes: 1000,
		Log:          fault.NewLog(),
		Index:        &index.Index{},
		Hasher:       report.NewHasher(),
		knownMissing: map[uint64]bool{},
	}
}

// MarkManifestMissing registers sequence numbers whose absence the
// manifest layer has already recorded.
func (p *Pipeline) MarkManifestMissing(seqs ...uint64) {
	if p.knownMissing == nil {
		p.knownMissing = map[uint64]bool{}
	}
	for _, s := range seqs {
		p.knownMissing[s] = true
	}
}

// Results returns a copy of accumulated per-segment results.
func (p *Pipeline) Results() []*Result {
	out := make([]*Result, len(p.results))
	copy(out, p.results)
	return out
}

// ParseStage runs structural parsing for one input. Missing segments are
// recorded as manifest faults and returned as damaged placeholders so the
// later stages can skip them without breaking ordering.
func (p *Pipeline) ParseStage(in SegmentInput) *ParsedSegment {
	ps := &ParsedSegment{Input: in, InputHash: report.Sum256(in.Data)}
	if !in.Present {
		ps.Damaged = true
		// The manifest layer already reported this gap with its source
		// offset; only add the parse-stage record when it did not.
		if !p.knownMissing[in.MediaSeq] {
			p.Log.AddUnique(fault.Record{
				Stage: fault.StageManifest, Severity: fault.SeverityError,
				Kind: fault.KindMissingSegment, Segment: in.Name,
				MediaSeq: in.MediaSeq, SourceOffset: in.ManifestOffset,
				Message: "segment listed in manifest but absent from inputs",
			})
		}
		return ps
	}
	res, err := stream.ParseMP4(in.Data)
	ps.MP4 = res
	ps.ParseErr = err
	if err != nil {
		ps.Damaged = true
		p.Log.Add(stream.RecordFromTruncation(in.Name, in.MediaSeq, err))
	}
	return ps
}

// Canonicalize renders a normalized segment to stable JSON bytes. Field
// order is fixed by the struct tags, so identical input yields identical
// output across runs.
func Canonicalize(ns *NormalizedSegment) ([]byte, error) {
	return json.MarshalIndent(ns, "", "  ")
}
