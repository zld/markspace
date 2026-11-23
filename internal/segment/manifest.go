package segment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
	"mediaverify/internal/subtitle"
)

// Stats is the pipeline summary embedded in every manifest.
type Stats struct {
	PacketsReceived   int `json:"packets_received"`
	PacketsDuplicate  int `json:"packets_duplicate"`
	PacketsReordered  int `json:"packets_reordered"`
	PacketsLost       int `json:"packets_lost"`
	DegradedIntervals int `json:"degraded_intervals"`
	ReplayCount       int `json:"replay_count"`
}

// SubtitleSpan associates a cue with every segment its original time
// interval touches. Times are always the cue's own start/end, never rewritten.
type SubtitleSpan struct {
	CueIndex     int   `json:"cue_index"`
	SegmentIndex []int `json:"segment_index"`
	StartNS      int64 `json:"start_ns"`
	EndNS        int64 `json:"end_ns"`
}

// RenditionManifest is one ladder rung; all rungs share the same cuts.
type RenditionManifest struct {
	TargetBPS int64     `json:"target_bps"`
	ActualBPS int64     `json:"actual_bps"`
	SegmentNS [][]int64 `json:"segment_start_end_ns"`
}

// Manifest is the deterministic, hashable delivery document.
type Manifest struct {
	TimeBasis         string              `json:"time_basis"`
	TickRateNS        int64               `json:"tick_rate_ns"`
	FrameStandard     string              `json:"frame_standard"`
	InputDigest       string              `json:"input_digest"`
	ContainerBrand    string              `json:"container_brand"`
	ContainerTimebase int64               `json:"container_timescale_hz"`
	ContainerDigest   string              `json:"container_sha256"`
	Stats             Stats               `json:"stats"`
	Renditions        []RenditionManifest `json:"renditions"`
	SubtitleSpans     []SubtitleSpan      `json:"subtitle_spans"`
}

// ContainerInfo carries the probed MP4 container metadata into the manifest.
type ContainerInfo struct {
	Brand     string
	Timescale int64
	SHA256    string
}

// BuildManifest assembles the shared document. The cut times come from the
// single plan, so rendition alignment can never drift beyond one frame.
func BuildManifest(fr clock.FrameRate, inputDigest string, container ContainerInfo, plan Plan, rungs []RenditionInput, cues []subtitle.Cue, stats Stats) (Manifest, error) {
	if len(rungs) < 2 {
		return Manifest{}, errs.New(errs.KindInvalidInput, "manifest", "need at least two renditions")
	}
	m := Manifest{
		TimeBasis:         "nanoseconds-since-first-rtp",
		TickRateNS:        1_000_000_000,
		FrameStandard:     fr.Name,
		InputDigest:       inputDigest,
		ContainerBrand:    container.Brand,
		ContainerTimebase: container.Timescale,
		ContainerDigest:   container.SHA256,
		Stats:             stats,
	}
	cuts := make([][]int64, len(plan.Segments))
	for i, s := range plan.Segments {
		cuts[i] = []int64{s.Start.Ns(), s.End.Ns()}
	}
	for _, rung := range rungs {
		m.Renditions = append(m.Renditions, RenditionManifest{
			TargetBPS: rung.TargetBPS, ActualBPS: rung.ActualBPS, SegmentNS: cuts,
		})
	}
	m.SubtitleSpans = associateCues(plan, cues)
	return m, nil
}

// RenditionInput carries one ladder rung's allocated rates.
type RenditionInput struct {
	TargetBPS int64
	ActualBPS int64
}

// associateCues maps each cue to every overlapping segment, preserving the
// cue's original time interval verbatim.
func associateCues(plan Plan, cues []subtitle.Cue) []SubtitleSpan {
	var spans []SubtitleSpan
	for _, cue := range cues {
		var hits []int
		for _, s := range plan.Segments {
			if cue.Start < s.End && cue.End > s.Start {
				hits = append(hits, s.Index)
			}
		}
		if len(hits) == 0 {
			continue
		}
		spans = append(spans, SubtitleSpan{
			CueIndex:     cue.Index,
			SegmentIndex: hits,
			StartNS:      cue.Start.Ns(),
			EndNS:        cue.End.Ns(),
		})
	}
	return spans
}

// deterministicJSON marshals with sorted keys and no HTML escaping surprises.
func deterministicJSON(m Manifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// Hash returns the SHA-256 of the canonical manifest encoding.
func (m Manifest) Hash() (string, error) {
	raw, err := deterministicJSON(m)
	if err != nil {
		return "", errs.New(errs.KindInternal, "manifest", "marshal: %v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// WriteAtomic writes the manifest to path via a temp file and rename, so a
// failed stage can never leave a partially written (polluted) output.
func (m Manifest) WriteAtomic(path string) error {
	raw, err := deterministicJSON(m)
	if err != nil {
		return errs.New(errs.KindInternal, "manifest", "marshal: %v", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errs.New(errs.KindInternal, "manifest", "mkdir: %v", err)
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return errs.New(errs.KindInternal, "manifest", "temp file: %v", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return errs.New(errs.KindInternal, "manifest", "write: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return errs.New(errs.KindInternal, "manifest", "sync: %v", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return errs.New(errs.KindInternal, "manifest", "close: %v", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return errs.New(errs.KindInternal, "manifest", "rename: %v", err)
	}
	return nil
}
