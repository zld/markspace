package demo

import (
	"encoding/json"
	"time"

	"mediad/internal/batch"
)

// Options are the deterministic demo/acceptance parameters.
type Options struct {
	Out            string        `json:"out"`
	Seed           int64         `json:"seed"`
	DriftPPM       float64       `json:"drift_ppm"`
	WrapAt         uint32        `json:"wrap_at"`
	VFR            bool          `json:"vfr"`
	Chunk          time.Duration `json:"-"`
	ChunkString    string        `json:"chunk"`
	TargetRate     int           `json:"target_rate"`
	LayoutName     string        `json:"layout"`
	LoudnessTarget float64       `json:"loudness_target"`
	PeakCeilingDB  float64       `json:"peak_ceiling_db"`

	// Fault injection flags used by the negative acceptance command.
	CorruptIndex  bool `json:"-"`
	TruncateChunk bool `json:"-"`
	LateOverlap   bool `json:"-"`
}

// DefaultOptions returns the normal acceptance configuration.
func DefaultOptions() Options {
	return Options{
		Out:            "/tmp/mediad-demo",
		Seed:           23,
		DriftPPM:       120,
		WrapAt:         4294967000,
		VFR:            true,
		Chunk:          2 * time.Second,
		ChunkString:    "2s",
		TargetRate:     48000,
		LayoutName:     "stereo",
		LoudnessTarget: -16,
		PeakCeilingDB:  -1.0,
	}
}

// Fingerprint computes the parameter identity used by checkpoints. Fault
// injection actions are deliberately excluded: they simulate corruption of
// an otherwise identical processing configuration.
func (o Options) Fingerprint() (string, error) {
	o.ChunkString = o.Chunk.String()
	type fp struct {
		Version        string  `json:"version"`
		Seed           int64   `json:"seed"`
		DriftPPM       float64 `json:"drift_ppm"`
		WrapAt         uint32  `json:"wrap_at"`
		VFR            bool    `json:"vfr"`
		ChunkNs        int64   `json:"chunk_ns"`
		TargetRate     int     `json:"target_rate"`
		LayoutName     string  `json:"layout"`
		LoudnessTarget float64 `json:"loudness_target"`
		PeakCeilingDB  float64 `json:"peak_ceiling_db"`
	}
	payload, err := json.Marshal(fp{
		Version:        "mediad-demo-v1",
		Seed:           o.Seed,
		DriftPPM:       o.DriftPPM,
		WrapAt:         o.WrapAt,
		VFR:            o.VFR,
		ChunkNs:        o.Chunk.Nanoseconds(),
		TargetRate:     o.TargetRate,
		LayoutName:     o.LayoutName,
		LoudnessTarget: o.LoudnessTarget,
		PeakCeilingDB:  o.PeakCeilingDB,
	})
	if err != nil {
		return "", err
	}
	return batch.FingerprintBytes(payload), nil
}
