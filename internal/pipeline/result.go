package pipeline

// Config configures one deterministic demo run.
type Config struct {
	Seed              int64
	Loss              []int
	Reorder           int
	CheckpointEvery   int
	TruncateAt        int
	CheckpointPath    string
	OutputPath        string
	Resume            bool
	CorruptCheckpoint bool
	Unrecoverable     bool
	WorkDir           string
}

// Result is the machine-readable terminal report.
type Result struct {
	Packets         int            `json:"packets"`
	Duplicates      int            `json:"duplicates"`
	Reordered       int            `json:"reordered"`
	Recovered       int            `json:"recovered"`
	AvDriftMS       float64        `json:"av_drift_ms"`
	FirstBadOffset  int64          `json:"first_bad_offset"`
	ResumeMS        int64          `json:"resume_ms"`
	SHA256          string         `json:"sha256"`
	OutputPath      string         `json:"output_path,omitempty"`
	CheckpointPath  string         `json:"checkpoint_path,omitempty"`
	ErrorCodes      map[string]int `json:"error_codes,omitempty"`
	ExitCode        int            `json:"-"`
	ResumedBoundary int            `json:"resumed_boundary,omitempty"`
}
