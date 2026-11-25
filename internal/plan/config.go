// Package plan builds deterministic, explainable repackaging configurations.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"mediaflow/internal/policy"
)

// SchemaVersion is bumped when the canonical configuration shape changes.
const SchemaVersion = 1

// Config is the complete reproducible transformation decision for one item.
type Config struct {
	SchemaVersion          int             `json:"schema_version"`
	InputDigest            string          `json:"input_digest"`
	Policy                 policy.Target   `json:"policy"`
	Stats                  policy.Stats    `json:"stats"`
	Decision               policy.Decision `json:"decision"`
	ChunkSamples           int             `json:"chunk_samples"`
	NormalizeStart         bool            `json:"normalize_start"`
	RejectOutOfBoundsEdits bool            `json:"reject_out_of_bounds_edits"`
	Digest                 string          `json:"config_digest"`
}

// New evaluates policy and returns a configuration with its canonical digest attached.
func New(inputDigest string, target policy.Target, stats policy.Stats, chunkSamples int) (*Config, error) {
	if chunkSamples <= 0 {
		chunkSamples = 64
	}
	c := &Config{
		SchemaVersion:          SchemaVersion,
		InputDigest:            inputDigest,
		Policy:                 target,
		Stats:                  stats,
		Decision:               policy.Evaluate(inputDigest, stats, target),
		ChunkSamples:           chunkSamples,
		NormalizeStart:         true,
		RejectOutOfBoundsEdits: true,
	}
	digest, err := c.computeDigest()
	if err != nil {
		return nil, err
	}
	c.Digest = digest
	return c, nil
}

type digestEnvelope struct {
	SchemaVersion          int             `json:"schema_version"`
	InputDigest            string          `json:"input_digest"`
	Policy                 policy.Target   `json:"policy"`
	Stats                  policy.Stats    `json:"stats"`
	Decision               policy.Decision `json:"decision"`
	ChunkSamples           int             `json:"chunk_samples"`
	NormalizeStart         bool            `json:"normalize_start"`
	RejectOutOfBoundsEdits bool            `json:"reject_out_of_bounds_edits"`
}

func (c *Config) computeDigest() (string, error) {
	b, err := json.Marshal(digestEnvelope{
		SchemaVersion: c.SchemaVersion, InputDigest: c.InputDigest, Policy: c.Policy,
		Stats: c.Stats, Decision: c.Decision, ChunkSamples: c.ChunkSamples,
		NormalizeStart:         c.NormalizeStart,
		RejectOutOfBoundsEdits: c.RejectOutOfBoundsEdits,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CanonicalJSON returns stable JSON for checkpoints and audit records.
func (c *Config) CanonicalJSON() ([]byte, error) {
	type withStableDigest Config
	return json.MarshalIndent((*withStableDigest)(c), "", "  ")
}

// Explain renders human-readable decision reasons without hiding infeasible constraints.
func (c *Config) Explain() string {
	var b strings.Builder
	fmt.Fprintf(&b, "config %s\n", c.Digest)
	fmt.Fprintf(&b, "  quality=%.4f min=%.2f pass=%v feasible=%v\n",
		c.Decision.QualityScore, c.Policy.QualityMin, c.Decision.QualityPass, c.Decision.Feasible)
	fmt.Fprintf(&b, "  target_video_kbps=%d audio_kbps=%d total_kbps=%d budget_kbps=%d\n",
		c.Decision.TargetVideoKbps, c.Decision.TargetAudioKbps,
		c.Decision.TargetTotalKbps, c.Decision.MaximumTotalKbps)
	fmt.Fprintf(&b, "  required_total_kbps=%d budget_deviation=%.3fMbps chunks=%d\n",
		c.Decision.RequiredTotalKbps, c.Decision.BudgetDeviationMbps, c.ChunkSamples)
	for _, d := range c.Decision.Deviations {
		fmt.Fprintf(&b, "  deviation %s expected=%.4f actual=%.4f delta=%.4f%s\n",
			d.Constraint, d.Expected, d.Actual, d.Delta, unitSuffix(d.Unit))
	}
	for _, r := range c.Decision.Reasons {
		fmt.Fprintf(&b, "  reason: %s\n", r)
	}
	return b.String()
}

func unitSuffix(unit string) string {
	if unit == "" {
		return ""
	}
	return " " + unit
}
