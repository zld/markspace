package manifest

import (
	"mediaprep/internal/model"
	"mediaprep/internal/stream"
)

type Summary struct {
	Segments          int    `json:"segments"`
	Silent            int    `json:"silent"`
	Duplicates        int    `json:"duplicates"`
	MetadataConflicts int    `json:"metadata_conflicts"`
	QueuePeak         int64  `json:"queue_peak"`
	QueueCapacity     int    `json:"queue_capacity"`
	BufferPeakBytes   int64  `json:"buffer_peak_bytes"`
	BufferBudgetBytes int64  `json:"buffer_budget_bytes"`
	Verdict           string `json:"verdict"`
}

type SourceRef struct {
	Path  string `json:"path"`
	Track string `json:"track"`
	Role  string `json:"role"`
}

type Asset struct {
	Fingerprint string      `json:"fingerprint"`
	Role        string      `json:"role"`
	Size        int         `json:"size"`
	DuplicateOf string      `json:"duplicate_of,omitempty"`
	Sources     []SourceRef `json:"sources"`
}

type Interval struct {
	Start int64          `json:"start"`
	End   int64          `json:"end"`
	Scale model.Rational `json:"scale"`
}

type Segment struct {
	ID          string   `json:"id"`
	Track       string   `json:"track"`
	Role        string   `json:"role"`
	Index       int      `json:"index"`
	Interval    Interval `json:"interval"`
	FirstSample int      `json:"first_sample"`
	EndSample   int      `json:"end_sample"`
	ByteSize    int      `json:"byte_size"`
}

type Silence struct {
	Track       string   `json:"track"`
	Interval    Interval `json:"interval"`
	DurationMS  float64  `json:"duration_ms"`
	RMSDB       float64  `json:"rms_db"`
	RMSDBText   string   `json:"rms_db_text"`
	PeakDBFS    float64  `json:"peak_dbfs"`
	WindowCount int      `json:"window_count"`
}

// Document is the version-independent canonical manifest report.
type Document struct {
	SchemaVersion string            `json:"schema_version"`
	Root          string            `json:"root"`
	CreatedAt     string            `json:"created_at"`
	Summary       Summary           `json:"summary"`
	Assets        []Asset           `json:"assets"`
	Segments      []Segment         `json:"segments"`
	Silences      []Silence         `json:"silences"`
	Exceptions    []model.Exception `json:"exceptions"`
	Pipeline      stream.Metrics    `json:"pipeline"`
}

type Pointer struct {
	Version    string `json:"version"`
	File       string `json:"file"`
	SHA256     string `json:"sha256"`
	UpdatedAt  string `json:"updated_at"`
	RollbackOf string `json:"rollback_of,omitempty"`
}
