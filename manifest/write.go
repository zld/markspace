// Package manifest writes the deterministic segment manifest.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/zld/mediapipe/media"
	"github.com/zld/mediapipe/pipeline"
	rtprec "github.com/zld/mediapipe/rtp"
)

// Entry is one segment of one representation layer in the manifest.
type Entry struct {
	Number              int    `json:"number"`
	Layer               string `json:"layer"`
	StartUS             int64  `json:"start_us"`
	EndUS               int64  `json:"end_us"`
	Path                string `json:"path"`
	SHA256              string `json:"sha256"`
	Bytes               int64  `json:"bytes"`
	Samples             int    `json:"samples"`
	BoundaryDeviationUS int64  `json:"boundary_deviation_us"`
}

// Manifest is the signed, ordered list of output segments plus the inputs
// that produced them.
type Manifest struct {
	Version      string               `json:"version"`
	InputSHA256  string               `json:"input_sha256"`
	IndexDigest  string               `json:"index_digest"`
	ParamsDigest string               `json:"params_digest"`
	CheckpointID int64                `json:"checkpoint_id"`
	SegmentCount int                  `json:"segment_count"`
	Segment      media.Time           `json:"-"`
	SegmentUS    int64                `json:"segment_us"`
	Entries      []Entry              `json:"entries"`
	AVSync       *pipeline.SyncReport `json:"av_sync,omitempty"`
	RTP          *rtprec.Result       `json:"rtp,omitempty"`
	Issues       []media.Issue        `json:"issues,omitempty"`
	Digest       string               `json:"digest"`
}

// Build assembles a manifest from segment outputs and planned boundaries.
// Entries are sorted by segment number then layer name, which makes the
// canonical digest independent of execution order.
func Build(inputSHA, indexDigest, paramsDigest string, checkpointID int64,
	seg media.Time, plan []pipeline.PlannedSegment, outs []pipeline.SegmentOutput,
	sync *pipeline.SyncReport) (*Manifest, error) {

	m := &Manifest{
		Version: "manifest-v1", InputSHA256: inputSHA,
		IndexDigest: indexDigest, ParamsDigest: paramsDigest,
		CheckpointID: checkpointID, Segment: seg, SegmentUS: seg.Micros(),
		SegmentCount: len(plan), AVSync: sync,
	}
	planByNum := make(map[int]pipeline.PlannedSegment, len(plan))
	for _, p := range plan {
		planByNum[p.Number] = p
	}
	for _, o := range outs {
		e := Entry{
			Number: o.Number, Layer: o.Layer,
			StartUS: o.StartUS, EndUS: o.EndUS,
			Path: o.Path, SHA256: o.SHA256, Bytes: o.Bytes, Samples: o.Samples,
		}
		if p, ok := planByNum[o.Number]; ok {
			e.BoundaryDeviationUS = p.MaxDeviationUS
		}
		m.Entries = append(m.Entries, e)
	}
	if err := m.normalize(); err != nil {
		return nil, err
	}
	return m, nil
}

// normalize sorts entries into canonical order and validates uniqueness.
func (m *Manifest) normalize() error {
	sort.SliceStable(m.Entries, func(i, j int) bool {
		if m.Entries[i].Number != m.Entries[j].Number {
			return m.Entries[i].Number < m.Entries[j].Number
		}
		return m.Entries[i].Layer < m.Entries[j].Layer
	})
	seen := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		key := fmt.Sprintf("%d/%s", e.Number, e.Layer)
		if seen[key] {
			return media.NewError(media.KindInternal, "", "duplicate manifest entry %s", key)
		}
		seen[key] = true
		// Entries from the same segment must share the shared boundary.
	}
	for _, p := range m.Entries {
		if p.StartUS < 0 || p.EndUS < 0 || p.EndUS < p.StartUS {
			return media.NewError(media.KindInternal, "", "invalid entry span %d..%d", p.StartUS, p.EndUS)
		}
	}
	d, err := m.digest()
	if err != nil {
		return err
	}
	m.Digest = d
	return nil
}

func (m *Manifest) digest() (string, error) {
	clone := *m
	clone.Digest = ""
	b, err := json.Marshal(&clone)
	if err != nil {
		return "", media.WrapError(media.KindInternal, "", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Canonical returns indented JSON for persistence.
func (m *Manifest) Canonical() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, media.WrapError(media.KindInternal, "", err)
	}
	return b, nil
}

// ParamsDigest hashes the execution parameters that affect output bytes.
func ParamsDigest(segment media.Time, bitrates []string, maxDeviation media.Time) string {
	b, _ := json.Marshal(struct {
		Segment      int64    `json:"segment_us"`
		Bitrates     []string `json:"bitrates"`
		MaxDeviation int64    `json:"max_deviation_us"`
	}{segment.Micros(), bitrates, maxDeviation.Micros()})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
