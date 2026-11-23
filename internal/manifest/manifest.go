// Package manifest renders the auditable archive manifest: per-layer coverage,
// gaps, bitrate, quality statistics and a deterministic SHA-256 digest.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"archivecheck/internal/media"
	"archivecheck/internal/recovery"
)

// Segment mirrors one recovered span.
type Segment struct {
	StartMs   int64 `json:"start_ms"`
	EndMs     int64 `json:"end_ms"`
	ByteStart int64 `json:"byte_start"`
	ByteEnd   int64 `json:"byte_end"`
}

// Gap mirrors one retained hole.
type Gap struct {
	StartMs   int64  `json:"start_ms"`
	EndMs     int64  `json:"end_ms"`
	ByteStart int64  `json:"byte_start"`
	ByteEnd   int64  `json:"byte_end"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
	Class     string `json:"class"`
}

// Layer is one representation's manifest row.
type Layer struct {
	Layer                  string    `json:"layer"`
	Codec                  string    `json:"codec"`
	Units                  int       `json:"units"`
	Keyframes              int       `json:"keyframes"`
	RecoveredSegments      int       `json:"recovered_segments"`
	RecoverableGaps        int       `json:"recoverable_gaps"`
	UnrecoverableGaps      int       `json:"unrecoverable_gaps"`
	StartMs                int64     `json:"start_ms"`
	EndMs                  int64     `json:"end_ms"`
	TargetKbps             float64   `json:"target_kbps"`
	ActualKbps             float64   `json:"actual_kbps"`
	BitrateErrorPct        float64   `json:"bitrate_error_pct"`
	ParseRatio             float64   `json:"parse_ratio"`
	MeanKeyframeIntervalMs float64   `json:"mean_keyframe_interval_ms"`
	MaxKeyframeIntervalMs  int64     `json:"max_keyframe_interval_ms"`
	Segments               []Segment `json:"segments"`
	Gaps                   []Gap     `json:"gaps"`
}

// Document is the complete manifest.
type Document struct {
	Version           int              `json:"version"`
	Case              string           `json:"case"`
	Seed              int64            `json:"seed"`
	ChunkBytes        int              `json:"chunk_bytes"`
	QueueDepth        int              `json:"queue_depth"`
	DurationMs        int64            `json:"duration_ms"`
	ToleranceMs       int64            `json:"tolerance_ms"`
	Layers            []Layer          `json:"layers"`
	BoundaryConflicts int              `json:"boundary_conflicts"`
	BoundaryDiffsMs   map[string]int64 `json:"boundary_diffs_ms"`
	ManifestDriftMs   int64            `json:"manifest_drift_ms"`
	Faults            []string         `json:"faults"`
	Quality           Quality          `json:"quality"`
	SHA256            string           `json:"sha256"`
}

// Quality aggregates cross-layer statistics.
type Quality struct {
	Layers                int     `json:"layers"`
	Segments              int     `json:"segments"`
	Gaps                  int     `json:"gaps"`
	MinParseRatio         float64 `json:"min_parse_ratio"`
	MaxBitrateErrorPct    float64 `json:"max_bitrate_error_pct"`
	MaxKeyframeIntervalMs int64   `json:"max_keyframe_interval_ms"`
	Complete              bool    `json:"complete"`
}

// LayerInput is the evidence needed to render one layer row.
type LayerInput struct {
	Result     recovery.Result
	Codec      string
	Units      int
	Keyframes  int
	TargetKbps float64
	ActualKbps float64
	ErrorPct   float64
}

// Build assembles a manifest document. The SHA-256 covers the canonical JSON of
// the document without the digest itself, so repeated runs over identical
// input must match.
func Build(doc Document, inputs []LayerInput, merge recovery.Merge) (Document, error) {
	doc.Layers = make([]Layer, 0, len(inputs))
	for _, in := range inputs {
		row := Layer{
			Layer: in.Result.Layer, Codec: in.Codec,
			Units: in.Units, Keyframes: in.Keyframes,
			RecoveredSegments: len(in.Result.Segments),
			StartMs:           in.Result.StartMs, EndMs: in.Result.EndMs,
			TargetKbps: in.TargetKbps, ActualKbps: in.ActualKbps, BitrateErrorPct: in.ErrorPct,
			ParseRatio:             in.Result.ParseRatio,
			MeanKeyframeIntervalMs: in.Result.MeanKFMs,
			MaxKeyframeIntervalMs:  in.Result.MaxKFMs,
		}
		for _, s := range in.Result.Segments {
			row.Segments = append(row.Segments, Segment{StartMs: s.StartMs, EndMs: s.EndMs, ByteStart: s.ByteStart, ByteEnd: s.ByteEnd})
		}
		for _, g := range in.Result.Gaps {
			row.Gaps = append(row.Gaps, Gap{StartMs: g.StartMs, EndMs: g.EndMs, ByteStart: g.ByteStart, ByteEnd: g.ByteEnd,
				Reason: string(g.Reason), Detail: g.Detail, Class: g.Class.String()})
			switch g.Class {
			case media.Recoverable:
				row.RecoverableGaps++
			case media.Unrecoverable:
				row.UnrecoverableGaps++
			}
		}
		doc.Layers = append(doc.Layers, row)
	}
	sort.SliceStable(doc.Layers, func(i, j int) bool { return doc.Layers[i].Layer < doc.Layers[j].Layer })

	doc.BoundaryConflicts = len(merge.Conflicts)
	doc.BoundaryDiffsMs = merge.LayerDiffs()
	doc.ManifestDriftMs = merge.DriftMs
	doc.Faults = append([]string(nil), doc.Faults...)
	sort.Strings(doc.Faults)

	q := Quality{Layers: len(doc.Layers), MinParseRatio: 1}
	for _, l := range doc.Layers {
		q.Segments += l.RecoveredSegments
		q.Gaps += l.RecoverableGaps + l.UnrecoverableGaps
		if l.ParseRatio < q.MinParseRatio {
			q.MinParseRatio = l.ParseRatio
		}
		if e := absf(l.BitrateErrorPct); e > q.MaxBitrateErrorPct {
			q.MaxBitrateErrorPct = e
		}
		if l.MaxKeyframeIntervalMs > q.MaxKeyframeIntervalMs {
			q.MaxKeyframeIntervalMs = l.MaxKeyframeIntervalMs
		}
	}
	q.Complete = q.Gaps == 0 && doc.BoundaryConflicts == 0
	doc.Quality = q

	sum, err := Digest(doc)
	if err != nil {
		return Document{}, err
	}
	doc.SHA256 = sum
	return doc, nil
}

// Digest returns the SHA-256 of the canonical JSON document excluding the
// digest field.
func Digest(doc Document) (string, error) {
	doc.SHA256 = ""
	canon, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// JSON renders the manifest as indented JSON.
func (doc Document) JSON() ([]byte, error) {
	return json.MarshalIndent(doc, "", "  ")
}

// Text renders a stable terminal summary. Required acceptance fields appear as
// key=value tokens so they can be asserted line by line.
func (doc Document) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "manifest version=%d case=%s seed=%d chunk_bytes=%d queue=%d duration_ms=%d tolerance_ms=%d\n",
		doc.Version, doc.Case, doc.Seed, doc.ChunkBytes, doc.QueueDepth, doc.DurationMs, doc.ToleranceMs)
	for _, l := range doc.Layers {
		fmt.Fprintf(&b, "layer=%s codec=%s units=%d keyframes=%d recovered_segments=%d recoverable_gaps=%d unrecoverable_gaps=%d\n",
			l.Layer, l.Codec, l.Units, l.Keyframes, l.RecoveredSegments, l.RecoverableGaps, l.UnrecoverableGaps)
		fmt.Fprintf(&b, "  window_ms=%d-%d target_kbps=%.2f actual_kbps=%.2f bitrate_error_pct=%+.2f parse_ratio=%.4f mean_keyframe_interval_ms=%.2f max_keyframe_interval_ms=%d\n",
			l.StartMs, l.EndMs, l.TargetKbps, l.ActualKbps, l.BitrateErrorPct, l.ParseRatio, l.MeanKeyframeIntervalMs, l.MaxKeyframeIntervalMs)
		for _, s := range l.Segments {
			fmt.Fprintf(&b, "  segment ms=%d-%d bytes=%d-%d\n", s.StartMs, s.EndMs, s.ByteStart, s.ByteEnd)
		}
		for _, g := range l.Gaps {
			fmt.Fprintf(&b, "  gap ms=%d-%d bytes=%d-%d reason=%s class=%s detail=%q\n", g.StartMs, g.EndMs, g.ByteStart, g.ByteEnd, g.Reason, g.Class, g.Detail)
		}
	}
	fmt.Fprintf(&b, "boundary_conflicts=%d manifest_drift_ms=%d\n", doc.BoundaryConflicts, doc.ManifestDriftMs)
	for _, layer := range sortedKeys(doc.BoundaryDiffsMs) {
		fmt.Fprintf(&b, "  boundary_diff layer=%s diff_ms=%d\n", layer, doc.BoundaryDiffsMs[layer])
	}
	fmt.Fprintf(&b, "quality layers=%d segments=%d gaps=%d min_parse_ratio=%.4f max_bitrate_error_pct=%.2f max_keyframe_interval_ms=%d complete=%t\n",
		doc.Quality.Layers, doc.Quality.Segments, doc.Quality.Gaps, doc.Quality.MinParseRatio, doc.Quality.MaxBitrateErrorPct, doc.Quality.MaxKeyframeIntervalMs, doc.Quality.Complete)
	for _, f := range doc.Faults {
		fmt.Fprintf(&b, "fault %s\n", f)
	}
	fmt.Fprintf(&b, "sha256=%s\n", doc.SHA256)
	return b.String()
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
