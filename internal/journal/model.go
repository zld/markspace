package journal

// Artifact is one immutable output file and its expected SHA-256.
type Artifact struct {
	Name string `json:"name"`
	Data []byte `json:"-"`
	SHA  string `json:"sha256"`
}

// FrameRecord is the manifest row for one review frame.
type FrameRecord struct {
	Sequence int      `json:"sequence"`
	File     string   `json:"file"`
	PTSNS    int64    `json:"pts_ns"`
	SHA256   string   `json:"sha256"`
	Sources  []string `json:"sources"`
}

// State is the atomically committed publication manifest.
type State struct {
	Schema       string        `json:"schema"`
	Case         string        `json:"case"`
	Transaction  string        `json:"transaction"`
	FrameCount   int           `json:"frame_count"`
	Frames       []FrameRecord `json:"frames"`
	InputSources []string      `json:"input_sources"`
	Artifacts    []ArtifactRow `json:"artifacts"`
}

// ArtifactRow records one non-frame output such as the checksum summary.
type ArtifactRow struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Report tells the CLI what commit/recovery did.
type Report struct {
	RecoveredArtifacts int    `json:"recovered_artifacts"`
	Published          int    `json:"published"`
	Duplicates         int    `json:"duplicates"`
	ReusedFiles        int    `json:"reused_files"`
	AlreadyCommitted   bool   `json:"already_committed"`
	StatePath          string `json:"state_path"`
}

const (
	stateFile     = "state.json"
	journalFile   = "journal.log"
	checksumsFile = "SHA256SUMS"
)
