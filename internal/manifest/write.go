// Package manifest writes the time-aligned analysis manifest. All times come
// from the unified integer clock; output bytes are deterministic so repeated
// runs over the same input produce byte-identical manifests.
package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"

	"continuity/internal/manifest/provenance"
	"continuity/internal/timeline"
)

// QualityMetrics reports objective audio quality indicators. The basis field
// states the computation methodology; when no decodable audio exists the
// metrics are marked not computable instead of being fabricated.
type QualityMetrics struct {
	Basis         string `json:"basis"`
	Computable    bool   `json:"computable"`
	Note          string `json:"note,omitempty"`
	PacketLossPPM int64  `json:"packetLossPPM"` // lost / (lost+received), parts per million
	Duplicates    int    `json:"duplicates"`
	GapCount      int    `json:"gapCount"`
	GapTicks      int64  `json:"gapTicks"`   // total missing audio, unified clock ticks
	CoveragePPM   int64  `json:"coveragePPM"` // received audio ticks / expected ticks
}

// TrackReport carries per-track timing and bitrate on the unified clock.
type TrackReport struct {
	Track            string `json:"track"`
	Kind             string `json:"kind"`
	Timescale        int64  `json:"timescale"`
	DurationTicks    int64  `json:"durationTicks"` // unified clock
	TargetBitrate    int64  `json:"targetBitrate"`
	ActualBitrate    int64  `json:"actualBitrate"`
	BitrateDeviation string `json:"bitrateDeviation"` // per-myriad of target, as "±x.xx%"
}

// KeyframeReport is one keyframe interval on the unified clock.
type KeyframeReport struct {
	Track       string `json:"track"`
	StartSample int    `json:"startSample"`
	EndSample   int    `json:"endSample"`
	StartTicks  int64  `json:"startTicks"`
	EndTicks    int64  `json:"endTicks"`
	LengthTicks int64  `json:"lengthTicks"`
	TooLong     bool   `json:"tooLong"`
}

// Provenance ties the manifest to its inputs and step outputs.
type Provenance struct {
	InputSHA256 map[string]string `json:"inputs"`
	StepSHA256  map[string]string `json:"steps"`
}

// Manifest is the top-level time-aligned report.
type Manifest struct {
	UnifiedTimescale    int64            `json:"unifiedTimescale"`
	Tracks              []TrackReport    `json:"tracks"`
	Damage              []timeline.Range `json:"damage"`
	Keyframes           []KeyframeReport `json:"keyframes"`
	Quality             QualityMetrics   `json:"quality"`
	AlignmentErrorTicks int64            `json:"alignmentErrorTicks"`
	Provenance          Provenance       `json:"provenance"`
}

// Bytes renders the manifest deterministically (struct field order, sorted
// map keys, fixed indentation, trailing newline).
func (m *Manifest) Bytes() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Write renders the manifest to path and returns its SHA-256.
func (m *Manifest) Write(path string) (string, error) {
	b, err := m.Bytes()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return provenance.Sum(b), nil
}
