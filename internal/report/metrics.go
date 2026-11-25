// Package report turns pipeline results into the deterministic JSON report
// printed by the mediavault adapter. It summarizes frame/segment hashes,
// pixel statistics, audio peaks, alignment error, boundary repairs and the
// manifest counts used by the acceptance checks.
package report

import (
	"encoding/json"

	"mediavault/internal/archive"
	"mediavault/internal/repair"
)

// Metrics is the full JSON-serializable run report.
type Metrics struct {
	Status           string             `json:"status"`
	Fault            string             `json:"fault"`
	ManifestEntries  int                `json:"manifest_entries"`
	ChunksCommitted  int                `json:"chunks_committed"`
	Tracks           []TrackMetrics     `json:"tracks"`
	Corrections      []repair.Correction `json:"corrections,omitempty"`
	FailedTracks     []repair.FailedTrack `json:"failed_tracks,omitempty"`
	Errors           []ErrorView        `json:"errors,omitempty"`
	MaxAlignmentNs   int64              `json:"max_alignment_error_ns"`
	SegmentCount     int                `json:"segment_count"`
	ContentDupes     int                `json:"content_duplicates"`
	TimelineDistinct int                `json:"timeline_distinct"`
	Recovered        bool               `json:"recovered_from_checkpoint,omitempty"`
}

// TrackMetrics aggregates one track's per-sample fingerprints.
type TrackMetrics struct {
	Track         string       `json:"track"`
	Kind          string       `json:"kind"`
	SampleCount   int          `json:"sample_count"`
	FrameHashes   []string     `json:"frame_sha256"`
	SegmentHashes []string     `json:"segment_sha256"`
	PixelMeanAvg  float64      `json:"pixel_mean_avg,omitempty"`
	PixelVarAvg   float64      `json:"pixel_variance_avg,omitempty"`
	PeakDBFS      float64      `json:"peak_dbfs,omitempty"`
	MaxAlignNs    int64        `json:"max_alignment_error_ns"`
	Rows          []archive.ManifestRow `json:"-"`
}

// ErrorView is the JSON projection of a timeline.StageError.
type ErrorView struct {
	Stage       string `json:"stage"`
	Kind        string `json:"kind"`
	Offset      int64  `json:"offset"`
	Position    uint64 `json:"position"`
	Track       string `json:"track"`
	Recoverable bool   `json:"recoverable"`
	Detail      string `json:"detail"`
}

// Builder accumulates metrics as stages produce rows.
type Builder struct {
	m Metrics
}

// NewBuilder starts a report for the named fault scenario.
func NewBuilder(fault string) *Builder {
	return &Builder{m: Metrics{Status: "ok", Fault: fault}}
}

// AddRows folds archived manifest rows into per-track metrics.
func (b *Builder) AddRows(rows []archive.ManifestRow) {
	byTrack := map[string]*TrackMetrics{}
	var order []string
	for _, r := range rows {
		tm, ok := byTrack[r.Track]
		if !ok {
			tm = &TrackMetrics{Track: r.Track, PeakDBFS: negInf}
			byTrack[r.Track] = tm
			order = append(order, r.Track)
		}
		tm.Rows = append(tm.Rows, r)
		tm.SampleCount++
		tm.FrameHashes = append(tm.FrameHashes, r.FrameSHA)
		if r.AlignmentNs > tm.MaxAlignNs {
			tm.MaxAlignNs = r.AlignmentNs
		}
		if r.AlignmentNs > b.m.MaxAlignmentNs {
			b.m.MaxAlignmentNs = r.AlignmentNs
		}
		if r.ContentDupe {
			b.m.ContentDupes++
		}
		if r.TimelineDistinct {
			b.m.TimelineDistinct++
		}
	}
	for _, name := range order {
		b.m.Tracks = append(b.m.Tracks, *summarize(byTrack[name]))
	}
	b.m.ManifestEntries = len(rows)
	b.m.SegmentCount = countSegments(rows)
}

// SetChunks records the number of committed chunks.
func (b *Builder) SetChunks(chunks []archive.Chunk) {
	b.m.ChunksCommitted = len(chunks)
}

// SetCorrections attaches boundary repairs.
func (b *Builder) SetCorrections(c []repair.Correction) {
	b.m.Corrections = c
}

// SetFailed attaches unrecoverable tracks.
func (b *Builder) SetFailed(f []repair.FailedTrack) {
	b.m.FailedTracks = f
}

// AddError records a stage error projection.
func (b *Builder) AddError(ev ErrorView) {
	b.m.Errors = append(b.m.Errors, ev)
}

// MarkRecovered flags a run that resumed from a checkpoint.
func (b *Builder) MarkRecovered() { b.m.Recovered = true }

// MarkFailed switches overall status to failed.
func (b *Builder) MarkFailed() { b.m.Status = "failed" }

// SetSegmentCount records the number of archived segments.
func (b *Builder) SetSegmentCount(n int) { b.m.SegmentCount = n }

// Build returns the finalized metrics.
func (b *Builder) Build() Metrics { return b.m }

// JSON renders the report as indented JSON.
func (m Metrics) JSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

const negInf = -1e308

func countSegments(rows []archive.ManifestRow) int {
	seen := map[string]bool{}
	for _, r := range rows {
		if r.SegmentSHA != "" {
			seen[r.SegmentSHA] = true
		}
	}
	return len(seen)
}

func summarize(tm *TrackMetrics) *TrackMetrics {
	var meanSum, varSum float64
	var meanN, varN int
	hasAudio := false
	var peak float64 = negInf
	segSeen := map[string]bool{}
	for _, r := range tm.Rows {
		if r.PixelMean != 0 {
			meanSum += r.PixelMean
			meanN++
		}
		if r.PixelVariance != 0 {
			varSum += r.PixelVariance
			varN++
		}
		if r.PeakDBFS != 0 {
			hasAudio = true
			if r.PeakDBFS > peak {
				peak = r.PeakDBFS
			}
		}
		if r.SegmentSHA != "" && !segSeen[r.SegmentSHA] {
			segSeen[r.SegmentSHA] = true
			tm.SegmentHashes = append(tm.SegmentHashes, r.SegmentSHA)
		}
	}
	if meanN > 0 {
		tm.PixelMeanAvg = meanSum / float64(meanN)
	}
	if varN > 0 {
		tm.PixelVarAvg = varSum / float64(varN)
	}
	if hasAudio {
		tm.PeakDBFS = peak
	}
	return tm
}
