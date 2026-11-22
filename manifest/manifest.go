// Package manifest assembles segment, silence, fingerprint and bandwidth
// results into a self-describing document whose aggregate statistics can be
// recomputed from its own entries.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"mediacore/index"
	"mediacore/media"
	"mediacore/pipeline"
)

// ManifestVersion is the manifest document version.
const ManifestVersion = 1

// SegmentEntry is the manifest's copy of one segment. Every field needed to
// recompute the aggregate frame count, duration and bitrate is present here.
type SegmentEntry struct {
	Index           int    `json:"index"`
	StartMS         int64  `json:"start_ms"`
	EndMS           int64  `json:"end_ms"`
	FrameCount      int    `json:"frame_count"`
	Bytes           int64  `json:"bytes"`
	GapCount        int    `json:"gap_count"`
	MissingFrames   int    `json:"missing_frames"`
	DuplicateFrames int    `json:"duplicate_frames"`
	SilenceMS       int64  `json:"silence_ms"`
	ClosedBy        string `json:"closed_by"`
	FirstSeq        uint32 `json:"first_seq"`
	LastSeq         uint32 `json:"last_seq"`
}

// Stats are the manifest aggregates. Verify recomputes each from the entries.
type Stats struct {
	FrameCount         int     `json:"frame_count"`
	SegmentCount       int     `json:"segment_count"`
	DurationMS         int64   `json:"duration_ms"`
	TotalBytes         int64   `json:"total_bytes"`
	GapCount           int     `json:"gap_count"`
	MissingFrames      int     `json:"missing_frames"`
	SilenceConfirmedMS int64   `json:"silence_confirmed_ms"`
	SilenceUnknownMS   int64   `json:"silence_unknown_ms"`
	DuplicateGroups    int     `json:"duplicate_groups"`
	DuplicateFrames    int     `json:"duplicate_frames"`
	Fingerprints       int     `json:"fingerprints"`
	Collisions         int     `json:"collisions"`
	TargetKBPS         int     `json:"target_kbps"`
	ActualKBPS         int     `json:"actual_kbps"`
	DeviationPct       float64 `json:"deviation_pct"`
}

// TimingBinding records how the manifest's media timing was cross-checked
// against the MP4 sample timing parsed by Eyevinn/mp4ff.
type TimingBinding struct {
	Source             string `json:"source"`
	Timescale          uint32 `json:"timescale"`
	SampleCount        int    `json:"sample_count"`
	MP4DurationTicks   int64  `json:"mp4_duration_ticks"`
	MediaDurationTicks int64  `json:"media_duration_ticks"`
	AlignmentErrorMS   int64  `json:"alignment_error_ms"`
	Checked            bool   `json:"checked"`
}

// Manifest is the complete, independently verifiable processing result.
type Manifest struct {
	Version         int                        `json:"version"`
	InputDigest     string                     `json:"input_digest"`
	IndexDigest     string                     `json:"index_digest"`
	Timescale       uint32                     `json:"timescale"`
	ManifestDigest  string                     `json:"manifest_digest"`
	Segments        []SegmentEntry             `json:"segments"`
	Silence         []pipeline.SilenceInterval `json:"silence"`
	DuplicateGroups []pipeline.DuplicateGroup  `json:"duplicate_groups"`
	Gaps            []pipeline.GapRef          `json:"gaps,omitempty"`
	Bandwidth       BandwidthTarget            `json:"bandwidth"`
	Timing          TimingBinding              `json:"timing"`
	Stats           Stats                      `json:"stats"`
	// MediaDurationMS is the span including the final frame's duration.
	MediaDurationMS int64 `json:"media_duration_ms"`
	// Issues carries non-fatal conditions (unknown silence, regressions).
	Issues []string `json:"issues,omitempty"`
}

// BandwidthTarget records the target/actual rate comparison.
type BandwidthTarget struct {
	TargetKBPS       int     `json:"target_kbps"`
	ActualKBPS       int     `json:"actual_kbps"`
	DeviationPct     float64 `json:"deviation_pct"`
	EstimateKBPS     float64 `json:"estimate_kbps"`
	InputCeilingKBPS float64 `json:"input_ceiling_kbps"`
	Reason           string  `json:"reason"`
}

// Options tunes manifest assembly.
type Options struct {
	// TargetKBPS is the recommended bitrate from the bandwidth estimator.
	TargetKBPS int
	// Bandwidth is the full estimator report, if available.
	Bandwidth *BandwidthReportLike
	// Timing is the MP4 timing cross-check result.
	Timing TimingBinding
	// MaxAlignmentMS fails verification when media/MP4 timing disagree more.
	MaxAlignmentMS int64
}

// BandwidthReportLike mirrors ingest.BandwidthReport without importing ingest
// into the manifest package.
type BandwidthReportLike struct {
	EstimateKBPS     float64 `json:"estimate_kbps"`
	InputCeilingKBPS float64 `json:"input_ceiling_kbps"`
	TargetKBPS       int     `json:"target_kbps"`
	Reason           string  `json:"reason"`
}

// Build assembles the manifest and computes all aggregates from entries.
func Build(idx *index.Index, res *pipeline.Result, opts Options) (*Manifest, error) {
	if idx == nil || res == nil {
		return nil, media.New(media.CatInvalidParams, media.StageManifest, "missing index or pipeline result")
	}
	m := &Manifest{
		Version:         ManifestVersion,
		InputDigest:     idx.InputDigest,
		IndexDigest:     idx.IndexDigest,
		Timescale:       res.Timescale,
		Segments:        make([]SegmentEntry, 0, len(res.Segments)),
		Silence:         res.Silence,
		DuplicateGroups: res.DuplicateGroups,
		Gaps:            res.Gaps,
		Timing:          opts.Timing,
		MediaDurationMS: res.MediaDurationMS,
	}
	// Attribute duplicates and silence to segments by media time.
	dupSeqs := make(map[uint32]bool)
	for _, g := range res.DuplicateGroups {
		for _, f := range g.Frames {
			if f.Role == "duplicate" {
				dupSeqs[f.Seq] = true
			}
		}
	}
	confirmed := pipeline.Confirmed(res.Silence)
	for _, s := range res.Segments {
		entry := SegmentEntry{
			Index:         s.Index,
			StartMS:       s.StartMS,
			EndMS:         s.EndMS,
			FrameCount:    s.FrameCount,
			Bytes:         s.Bytes,
			GapCount:      s.GapCount,
			MissingFrames: s.MissingFrames,
			ClosedBy:      s.ClosedBy,
			FirstSeq:      s.FirstSeq,
			LastSeq:       s.LastSeq,
		}
		for _, iv := range confirmed {
			entry.SilenceMS += overlapMS(iv, s)
		}
		m.Segments = append(m.Segments, entry)
	}
	for seq := range dupSeqs {
		for i := range m.Segments {
			if seqInRange(seq, m.Segments[i].FirstSeq, m.Segments[i].LastSeq) {
				m.Segments[i].DuplicateFrames++
				break
			}
		}
	}
	m.Stats.TargetKBPS = opts.TargetKBPS
	if opts.Bandwidth != nil {
		m.Bandwidth = BandwidthTarget{
			TargetKBPS:       opts.Bandwidth.TargetKBPS,
			EstimateKBPS:     opts.Bandwidth.EstimateKBPS,
			InputCeilingKBPS: opts.Bandwidth.InputCeilingKBPS,
			Reason:           opts.Bandwidth.Reason,
		}
		m.Stats.TargetKBPS = opts.Bandwidth.TargetKBPS
	}
	m.computeStats(res)
	m.ManifestDigest = m.ComputeDigest()
	return m, nil
}

func (m *Manifest) computeStats(res *pipeline.Result) {
	var st Stats
	st.TargetKBPS = m.Stats.TargetKBPS
	st.SegmentCount = len(m.Segments)
	for _, s := range m.Segments {
		st.FrameCount += s.FrameCount
		st.TotalBytes += s.Bytes
		st.GapCount += s.GapCount
		st.MissingFrames += s.MissingFrames
		st.DuplicateFrames += s.DuplicateFrames
		if s.EndMS > st.DurationMS {
			st.DurationMS = s.EndMS
		}
	}
	if len(m.Segments) > 0 {
		st.DurationMS = m.Segments[len(m.Segments)-1].EndMS - m.Segments[0].StartMS
	}
	if m.MediaDurationMS > st.DurationMS {
		st.DurationMS = m.MediaDurationMS
	}
	st.SilenceConfirmedMS = pipeline.TotalMS(m.Silence, pipeline.SilenceConfirmed)
	st.SilenceUnknownMS = pipeline.TotalMS(m.Silence, pipeline.SilenceUnknown)
	st.DuplicateGroups = len(m.DuplicateGroups)
	st.Fingerprints = res.FingerprintCount
	st.Collisions = res.Collisions
	st.ActualKBPS, st.DeviationPct = rates(st.TotalBytes, st.DurationMS, st.TargetKBPS)
	m.Bandwidth.ActualKBPS = st.ActualKBPS
	m.Bandwidth.DeviationPct = st.DeviationPct
	m.Stats = st
}

// rates converts bytes over a duration to kbps and the percent deviation from
// the target. A zero target yields zero deviation.
func rates(totalBytes, durationMS int64, targetKBPS int) (int, float64) {
	if durationMS <= 0 {
		return 0, 0
	}
	actual := float64(totalBytes) * 8 / float64(durationMS)
	actualKBPS := int(actual + 0.5)
	dev := 0.0
	if targetKBPS > 0 {
		dev = (actual - float64(targetKBPS)) / float64(targetKBPS) * 100
	}
	return actualKBPS, round2(dev)
}

func round2(v float64) float64 {
	if v >= 0 {
		return float64(int64(v*100+0.5)) / 100
	}
	return float64(int64(v*100-0.5)) / 100
}

func overlapMS(iv pipeline.SilenceInterval, s pipeline.Segment) int64 {
	start := iv.StartMS
	if s.StartMS > start {
		start = s.StartMS
	}
	end := iv.EndMS
	if s.EndMS < end {
		end = s.EndMS
	}
	if end <= start {
		return 0
	}
	return end - start
}

func seqInRange(seq, first, last uint32) bool {
	if first <= last {
		return seq >= first && seq <= last
	}
	return seq >= first || seq <= last
}

// ComputeDigest makes the manifest content-addressed while excluding the
// digest field itself.
func (m *Manifest) ComputeDigest() string {
	clone := *m
	clone.ManifestDigest = ""
	b, err := json.Marshal(&clone)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Marshal renders indented JSON with a trailing newline.
func (m *Manifest) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, media.Wrap(media.CatIO, media.StageManifest, err, "marshal manifest")
	}
	return append(b, '\n'), nil
}

// Summary renders the required single-line terminal summary.
func (m *Manifest) Summary() string {
	return fmt.Sprintf("frames=%d segments=%d silence_ms=%d duplicate_groups=%d",
		m.Stats.FrameCount, m.Stats.SegmentCount, m.Stats.SilenceConfirmedMS, m.Stats.DuplicateGroups)
}

// RateLine renders the required bitrate summary.
func (m *Manifest) RateLine() string {
	return fmt.Sprintf("target_kbps=%d actual_kbps=%d deviation_pct=%.2f",
		m.Stats.TargetKBPS, m.Stats.ActualKBPS, m.Stats.DeviationPct)
}
