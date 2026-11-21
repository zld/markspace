// Package config loads and validates hlsflow job descriptions.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"time"
)

// supportedCodec is the only elementary stream codec the pipeline accepts.
const supportedCodec = "h264"

// safeName matches rendition names that are safe to use as path components.
var safeName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// FaultInjection describes a deterministic corruption applied during verify.
// The corruption is applied to the byte stream read from disk in memory only;
// files on disk are never modified. It exists so the fixture scenario can
// reproduce checksum mismatches end to end.
type FaultInjection struct {
	Rendition string `json:"rendition"`
	Segment   int    `json:"segment"`
	Offset    int    `json:"offset"`
	XOR       byte   `json:"xor"`
}

// Rendition is one bitrate ladder rung.
type Rendition struct {
	Name    string `json:"name"`
	Bitrate int    `json:"bitrate"`
	Codec   string `json:"codec"`
	// FirstSeq and LastSeq are the inclusive source segment sequence range
	// declared by the operator.
	FirstSeq int `json:"first_seq"`
	LastSeq  int `json:"last_seq"`
}

// Job is the validated job configuration.
type Job struct {
	Renditions    []Rendition     `json:"renditions"`
	GapTimeout    Duration        `json:"gap_timeout"`
	DriftTolerance Duration       `json:"drift_tolerance"`
	Faults        []FaultInjection `json:"faults,omitempty"`
}

// Duration wraps time.Duration with millisecond JSON encoding.
type Duration struct {
	time.Duration
}

// MarshalJSON encodes the duration as a number of milliseconds.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%d", d.Milliseconds())), nil
}

// UnmarshalJSON accepts either a number of milliseconds or a "2s" string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var ms int64
	if err := json.Unmarshal(b, &ms); err == nil {
		d.Duration = time.Duration(ms) * time.Millisecond
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be milliseconds number or duration string: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// Load reads and validates a job file from path.
func Load(path string) (*Job, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read job: %w", err)
	}
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, fmt.Errorf("parse job: %w", err)
	}
	if err := job.Validate(); err != nil {
		return nil, err
	}
	return &job, nil
}

// Validate checks the whole job description.
func (j *Job) Validate() error {
	if len(j.Renditions) == 0 {
		return fmt.Errorf("job must declare at least one rendition")
	}
	names := make(map[string]bool, len(j.Renditions))
	bitrates := make(map[int]bool, len(j.Renditions))
	for i := range j.Renditions {
		r := &j.Renditions[i]
		if !safeName.MatchString(r.Name) {
			return fmt.Errorf("rendition %d: unsafe or empty name %q", i, r.Name)
		}
		if names[r.Name] {
			return fmt.Errorf("duplicate rendition name %q", r.Name)
		}
		names[r.Name] = true
		if r.Bitrate <= 0 {
			return fmt.Errorf("rendition %q: bitrate must be positive, got %d", r.Name, r.Bitrate)
		}
		if bitrates[r.Bitrate] {
			return fmt.Errorf("rendition %q: duplicate bitrate %d", r.Name, r.Bitrate)
		}
		bitrates[r.Bitrate] = true
		if r.Codec != supportedCodec {
			return fmt.Errorf("rendition %q: unsupported codec %q (only %s)", r.Name, r.Codec, supportedCodec)
		}
		if r.FirstSeq < 0 || r.LastSeq < r.FirstSeq {
			return fmt.Errorf("rendition %q: invalid sequence range [%d,%d]", r.Name, r.FirstSeq, r.LastSeq)
		}
	}
	if j.GapTimeout.Duration < 0 {
		return fmt.Errorf("gap_timeout must be non-negative")
	}
	if j.DriftTolerance.Duration < 0 {
		return fmt.Errorf("drift_tolerance must be non-negative")
	}
	for i := range j.Faults {
		f := &j.Faults[i]
		if !names[f.Rendition] {
			return fmt.Errorf("fault %d: unknown rendition %q", i, f.Rendition)
		}
	}
	return nil
}

// Rendition looks up a rendition by name.
func (j *Job) Rendition(name string) *Rendition {
	for i := range j.Renditions {
		if j.Renditions[i].Name == name {
			return &j.Renditions[i]
		}
	}
	return nil
}

// ExpectedCount returns the number of source segments declared for r.
func (r Rendition) ExpectedCount() int {
	return r.LastSeq - r.FirstSeq + 1
}
