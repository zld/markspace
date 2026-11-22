package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/archive/mediakernel/internal/timeline"
)

// Segment is one recovered, cuttable archive segment.
type Segment struct {
	Index        int    `json:"index"`
	StartUS      int64  `json:"start_us"`
	EndUS        int64  `json:"end_us"`
	KeyframeUS   int64  `json:"keyframe_us"`        // actual decodable boundary used
	BoundaryDevUS int64 `json:"boundary_dev_us"`    // requested cut vs keyframe
	Bytes        int64  `json:"bytes"`
	Fingerprint  string `json:"fingerprint"`
	SourceAsset  string `json:"source_asset"`
	SourceRanges []Range `json:"source_ranges,omitempty"` // provenance incl. repeats
}

// Range is a source-media interval in unified timeline coordinates.
type Range struct {
	StartUS int64 `json:"start_us"`
	EndUS   int64 `json:"end_us"`
}

// Silence status values. Unknown material always carries an explicit non-silent
// status rather than being reported as silence.
const (
	StatusSilent          = "silent"
	StatusPartialWindow   = "partial-window"
	StatusTimestampRegress = "timestamp-regress"
	StatusGapCovered      = "gap-covered"
)

// Silence is a detected silence interval. Status explains why an interval is or
// is not classified as silence; unknown intervals are never reported silent.
type Silence struct {
	StartUS int64  `json:"start_us"`
	EndUS   int64  `json:"end_us"`
	Status  string `json:"status"` // silent | partial-window | timestamp-regress | gap-covered
}

// FingerprintRecord ties a fingerprint to the timeline range it covers.
type FingerprintRecord struct {
	Hash       string  `json:"hash"`
	StartUS    int64  `json:"start_us"`
	EndUS      int64  `json:"end_us"`
	Duplicate  bool   `json:"duplicate"`
	SourceRanges []Range `json:"source_ranges"`
}

// LossGap is a confirmed burst loss on the recovered stream.
type LossGap struct {
	StartUS      int64 `json:"start_us"`
	EndUS        int64 `json:"end_us"`
	MissingCount int   `json:"missing_count"`
}

// ResultManifest is the recovery output for one full pipeline run. All time
// fields are unified-axis microseconds; sorted order is part of the contract
// and verified on every run.
type ResultManifest struct {
	SchemaVersion int                 `json:"schema_version"`
	InputDigest   string              `json:"input_digest"`
	IndexRebuilt  bool                `json:"index_rebuilt"`
	Segments      []Segment           `json:"segments"`
	Silences      []Silence           `json:"silences"`
	Fingerprints  []FingerprintRecord `json:"fingerprints"`
	Gaps          []LossGap           `json:"loss_gaps"`
	Checkpoint    *Checkpoint         `json:"checkpoint,omitempty"`
}

// Checkpoint records the last fully completed segment so an interrupted run can
// resume without re-emitting or duplicating earlier output.
type Checkpoint struct {
	CompletedSegments int   `json:"completed_segments"`
	LastSequence      uint16 `json:"last_sequence"`
	LastTimelineUS    int64 `json:"last_timeline_us"`
}

// WriteFile validates, serializes and atomically writes a result manifest. The
// destination must pass the same path-safety rules as inputs.
func WriteFile(path, baseDir string, r *ResultManifest) error {
	if r == nil {
		return timeline.NewError(timeline.KindInternal, "nil result manifest")
	}
	if err := safePath(path, baseDir); err != nil {
		return timeline.WrapError(timeline.KindInvalidInput, "output path unsafe", err)
	}
	if err := r.validate(); err != nil {
		return err
	}
	full := filepath.Join(baseDir, filepath.Clean(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return timeline.WrapError(timeline.KindInternal, "output directory creation failed", err)
	}
	tmp := full + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return timeline.WrapError(timeline.KindInternal, "output open failed", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		f.Close()
		os.Remove(tmp)
		return timeline.WrapError(timeline.KindInternal, "output encode failed", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return timeline.WrapError(timeline.KindInternal, "output sync failed", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return timeline.WrapError(timeline.KindInternal, "output close failed", err)
	}
	if err := os.Rename(tmp, full); err != nil {
		os.Remove(tmp)
		return timeline.WrapError(timeline.KindInternal, "atomic rename failed", err)
	}
	return nil
}

func (r *ResultManifest) validate() error {
	if r.SchemaVersion != 1 {
		return timeline.NewError(timeline.KindInternal, "result schema version %d invalid", r.SchemaVersion)
	}
	if r.InputDigest == "" {
		return timeline.NewError(timeline.KindInternal, "input_digest missing")
	}
	for i, s := range r.Segments {
		if s.StartUS < 0 || s.EndUS <= s.StartUS {
			return timeline.NewError(timeline.KindInternal, "segment %d has invalid time range", i)
		}
		if i > 0 && s.StartUS < r.Segments[i-1].StartUS {
			return timeline.NewError(timeline.KindInternal, "segments not sorted by start time")
		}
	}
	for i, s := range r.Silences {
		if s.EndUS <= s.StartUS {
			return timeline.NewError(timeline.KindInternal, "silence %d has invalid range", i)
		}
		if i > 0 && s.StartUS < r.Silences[i-1].StartUS {
			return timeline.NewError(timeline.KindInternal, "silences not sorted by start time")
		}
	}
	for i, f := range r.Fingerprints {
		if f.Hash == "" || f.EndUS <= f.StartUS {
			return timeline.NewError(timeline.KindInternal, "fingerprint %d invalid", i)
		}
		if i > 0 && f.StartUS < r.Fingerprints[i-1].StartUS {
			return timeline.NewError(timeline.KindInternal, "fingerprints not sorted by start time")
		}
	}
	for i, g := range r.Gaps {
		if g.MissingCount <= 0 || g.EndUS <= g.StartUS {
			return timeline.NewError(timeline.KindInternal, "gap %d invalid", i)
		}
		if i > 0 && g.StartUS < r.Gaps[i-1].StartUS {
			return timeline.NewError(timeline.KindInternal, "gaps not sorted by start time")
		}
	}
	return nil
}

// String gives a compact deterministic summary for terminal output.
func (r *ResultManifest) String() string {
	return fmt.Sprintf("digest=%s segments=%d silences=%d fingerprints=%d gaps=%d rebuilt=%t",
		r.InputDigest, len(r.Segments), len(r.Silences), len(r.Fingerprints), len(r.Gaps), r.IndexRebuilt)
}
