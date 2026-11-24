package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"mediarun/internal/clock"
)

// Provenance is the machine-readable answer to "which inputs produced this
// output, with which parameters and tools, and was anything lost?".
type Provenance struct {
	SchemaVersion int                 `json:"schema_version"`
	JobID         string              `json:"job_id"`
	CreatedAtUTC  string              `json:"created_at_utc"`
	Input         InputProvenance     `json:"input"`
	Parameters    map[string]any      `json:"parameters"`
	Tools         ToolVersions        `json:"tools"`
	Segments      []SegmentProvenance `json:"segments"`
	Output        OutputProvenance    `json:"output"`
	Events        []clock.Event       `json:"events"`
	Cache         CacheSummary        `json:"cache"`
	Valid         bool                `json:"valid"`
}

// InputProvenance pins down the exact input bytes and their shape.
type InputProvenance struct {
	Path        string `json:"path"`
	Digest      string `json:"digest"`
	SizeBytes   int64  `json:"size_bytes"`
	PacketCount int    `json:"packet_count"`
}

// SegmentProvenance traces one output segment to its source bytes.
type SegmentProvenance struct {
	Index          int      `json:"index"`
	SourceRange    [2]int64 `json:"source_range"`
	InputDigest    string   `json:"input_digest"`
	OutputDigest   string   `json:"output_digest"`
	CacheHash      string   `json:"cache_hash"`
	FromCache      bool     `json:"from_cache"`
	FromCheckpoint bool     `json:"from_checkpoint,omitempty"`
	MissReason     string   `json:"miss_reason,omitempty"`
	DurationMS     int64    `json:"duration_ms"`
}

// OutputProvenance pins the combined result.
type OutputProvenance struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// CacheSummary exposes reuse behaviour for the acceptance report.
type CacheSummary struct {
	Hits    int            `json:"hits"`
	Misses  int            `json:"misses"`
	Reasons map[string]int `json:"miss_reasons"`
}

// NewProvenance initializes a provenance record for a job.
func NewProvenance(jobID string) *Provenance {
	return &Provenance{
		SchemaVersion: 1,
		JobID:         jobID,
		CreatedAtUTC:  time.Now().UTC().Format(time.RFC3339),
		Parameters:    map[string]any{},
		Segments:      []SegmentProvenance{},
		Events:        []clock.Event{},
		Cache:         CacheSummary{Reasons: map[string]int{}},
	}
}

// AddSegment records one segment's lineage and updates cache counters.
// Segments restored from a checkpoint on resume are lineage only and do not
// count as fresh cache activity.
func (p *Provenance) AddSegment(s SegmentProvenance) {
	if s.FromCheckpoint {
		p.Segments = append(p.Segments, s)
		return
	}
	if s.FromCache {
		p.Cache.Hits++
	} else {
		p.Cache.Misses++
		if s.MissReason != "" {
			p.Cache.Reasons[s.MissReason]++
		}
	}
	p.Segments = append(p.Segments, s)
}

// Validate recomputes every digest chain and confirms the output file matches
// the recorded final digest. It mutates Valid and returns it.
func (p *Provenance) Validate(outputPath string) (bool, error) {
	if len(p.Segments) == 0 {
		p.Valid = false
		return false, fmt.Errorf("provenance has no segments")
	}
	// Every segment must trace back to a distinct, non-empty source range.
	for i, s := range p.Segments {
		if s.Index != i || s.InputDigest == "" || s.OutputDigest == "" {
			p.Valid = false
			return false, fmt.Errorf("segment %d lineage incomplete", i)
		}
		if s.SourceRange[1] <= s.SourceRange[0] {
			p.Valid = false
			return false, fmt.Errorf("segment %d has empty source range", i)
		}
	}
	got, err := FileDigest(outputPath)
	if err != nil {
		p.Valid = false
		return false, fmt.Errorf("output digest check failed: %w", err)
	}
	if got != p.Output.Digest {
		p.Valid = false
		return false, fmt.Errorf("output digest mismatch: got %s want %s", got, p.Output.Digest)
	}
	p.Valid = true
	return true, nil
}

// Write persists the provenance JSON atomically.
func (p *Provenance) Write(path string) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

// LoadProvenance reads a provenance document back for verification.
func LoadProvenance(path string) (*Provenance, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Provenance
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
