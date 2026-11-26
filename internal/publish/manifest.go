package publish

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"regress/internal/fault"
)

// ManifestEntry is one media segment as recorded in the delivery manifest.
type ManifestEntry struct {
	Sequence      uint32 `json:"sequence"`
	File          string `json:"file"`
	BaseTimeTicks uint64 `json:"base_time_ticks"`
	DurationTicks uint64 `json:"duration_ticks"`
	SampleCount   uint32 `json:"sample_count"`
	SHA256        string `json:"sha256"`
}

// Manifest is the deterministic, self-describing delivery manifest. Every
// timing field is sourced from go-mp4 re-parsing, never from producer memory.
type Manifest struct {
	Version     int             `json:"version"`
	Codec       string          `json:"codec"`
	JobKey      string          `json:"job_key"`
	ConfigHash  string          `json:"config_hash"`
	Timescale   uint32          `json:"timescale"`
	InitFile    string          `json:"init_file"`
	InitSHA256  string          `json:"init_sha256"`
	Segments    []ManifestEntry `json:"segments"`
}

// BuildManifest assembles a manifest from independently parsed segments and
// verifies continuity while doing so: sequence numbers must be contiguous and
// each segment's base time must equal the previous segment's end time.
func BuildManifest(codec, jobKey, configHash, initFile string, initHash string,
	metas []*SegmentMeta, hashes []string) (*Manifest, error) {
	m := &Manifest{
		Version:    1,
		Codec:      codec,
		JobKey:     jobKey,
		ConfigHash: configHash,
		InitFile:   filepath.Base(initFile),
		InitSHA256: initHash,
	}
	if len(metas) == 0 {
		return nil, fault.New(fault.StagePublish, fault.CodeManifestInconsistent, codec,
			"no segments to manifest")
	}
	m.Timescale = metas[0].Timescale
	var expectedEnd uint64
	baseSeq := metas[0].Sequence // encoders conventionally start at 0 or 1
	for i, meta := range metas {
		if meta.Sequence != baseSeq+uint32(i) {
			return nil, fault.At(fault.StagePublish, fault.CodeBoundaryDiscontinuity, codec,
				int64(meta.BaseTime),
				fmt.Sprintf("segment %d sequence %d not contiguous (base %d)", i, meta.Sequence, baseSeq))
		}
		if i > 0 && meta.BaseTime != expectedEnd {
			return nil, fault.At(fault.StagePublish, fault.CodeBoundaryDiscontinuity, codec,
				int64(meta.BaseTime),
				fmt.Sprintf("segment %d base %d != expected end %d", i, meta.BaseTime, expectedEnd))
		}
		m.Segments = append(m.Segments, ManifestEntry{
			Sequence:      meta.Sequence,
			File:          filepath.Base(meta.Path),
			BaseTimeTicks: meta.BaseTime,
			DurationTicks: meta.DurationTicks,
			SampleCount:   meta.SampleCount,
			SHA256:        hashes[i],
		})
		expectedEnd = meta.BaseTime + meta.DurationTicks
	}
	return m, nil
}

// WriteManifest serializes the manifest deterministically (indented, sorted
// map-free struct) to the publish directory.
func WriteManifest(m *Manifest, path string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fault.New(fault.StagePublish, fault.CodeIO, m.Codec, err.Error()).Wrap(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fault.New(fault.StagePublish, fault.CodeIO, m.Codec, err.Error()).Wrap(err)
	}
	return nil
}

// ReadManifest parses a manifest back for independent verification.
func ReadManifest(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.New(fault.StageVerify, fault.CodeIO, "", err.Error()).Wrap(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fault.New(fault.StageVerify, fault.CodeManifestInconsistent, "",
			"manifest parse failed: "+err.Error()).Wrap(err)
	}
	return &m, nil
}
