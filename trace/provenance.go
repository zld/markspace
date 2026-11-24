// Package trace records where every output came from.
package trace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/zld/mediapipe/media"
)

// Source identifies an input that contributed to an output.
type Source struct {
	Role     string `json:"role"` // "media" or "rtp"
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Index    string `json:"index_digest,omitempty"`
	Segments int    `json:"segments"`
}

// SegmentTrace links one output segment to its inputs and checkpoint.
type SegmentTrace struct {
	Number       int           `json:"number"`
	Layer        string        `json:"layer"`
	StartUS      int64         `json:"start_us"`
	EndUS        int64         `json:"end_us"`
	OutputPath   string        `json:"output_path"`
	OutputSHA256 string        `json:"output_sha256"`
	InputPaths   []string      `json:"input_paths"`
	InputSHA256  []string      `json:"input_sha256"`
	IndexDigest  string        `json:"index_digest"`
	ParamsDigest string        `json:"params_digest"`
	CheckpointID int64         `json:"checkpoint_id"`
	Issues       []media.Issue `json:"issues,omitempty"`
}

// Provenance is the complete per-run trace, written beside the manifest.
type Provenance struct {
	Version      string         `json:"version"`
	RunID        string         `json:"run_id"`
	Sources      []Source       `json:"sources"`
	RTP          *RTPTrace      `json:"rtp,omitempty"`
	Segments     []SegmentTrace `json:"segments"`
	ParamsDigest string         `json:"params_digest"`
	CheckpointID int64          `json:"checkpoint_id"`
	Issues       []media.Issue  `json:"issues,omitempty"`
	Digest       string         `json:"digest"`
}

// RTPTrace records packet recovery for a run.
type RTPTrace struct {
	SourcePath       string        `json:"source_path"`
	SourceSHA256     string        `json:"source_sha256"`
	SentPackets      int           `json:"sent_packets"`
	ReceivedPackets  int           `json:"received_packets"`
	RecoveredPackets int           `json:"recovered_packets"`
	LostPackets      int           `json:"lost_packets"`
	DuplicatePackets int           `json:"duplicate_packets"`
	RecoveryRate     float64       `json:"recovery_rate"`
	GapCount         int           `json:"gap_count"`
	Issues           []media.Issue `json:"issues,omitempty"`
}

// Build assembles provenance and computes its digest.
func Build(runID string, sources []Source, segs []SegmentTrace, paramsDigest string, checkpointID int64) (*Provenance, error) {
	p := &Provenance{
		Version: "provenance-v1", RunID: runID,
		Sources: sources, Segments: segs,
		ParamsDigest: paramsDigest, CheckpointID: checkpointID,
	}
	sort.SliceStable(p.Segments, func(i, j int) bool {
		if p.Segments[i].Number != p.Segments[j].Number {
			return p.Segments[i].Number < p.Segments[j].Number
		}
		return p.Segments[i].Layer < p.Segments[j].Layer
	})
	sort.SliceStable(p.Sources, func(i, j int) bool {
		if p.Sources[i].Role != p.Sources[j].Role {
			return p.Sources[i].Role < p.Sources[j].Role
		}
		return p.Sources[i].Path < p.Sources[j].Path
	})
	d, err := p.digest()
	if err != nil {
		return nil, err
	}
	p.Digest = d
	return p, nil
}

func (p *Provenance) digest() (string, error) {
	clone := *p
	clone.Digest = ""
	b, err := json.Marshal(&clone)
	if err != nil {
		return "", media.WrapError(media.KindInternal, "", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Canonical renders indented JSON for persistence.
func (p *Provenance) Canonical() ([]byte, error) {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, media.WrapError(media.KindInternal, "", err)
	}
	return b, nil
}
