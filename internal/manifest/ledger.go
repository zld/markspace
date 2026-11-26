// Package manifest defines the durable ledger of published artifacts and the
// traceable anomaly list. The manifest carries a format version so future
// readers can detect incompatible on-disk generations, and every artifact
// records its source interval offsets plus length/hash/commit state.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	merr "mediapack/internal/errors"
	"mediapack/internal/safepath"
)

// FormatVersion is the ledger format generation written by this build.
const FormatVersion = 1

// ManifestName is the ledger file inside the work directory.
const ManifestName = "manifest.json"

// Status is an artifact commit state.
type Status string

const (
	StatusPending   Status = "pending"
	StatusPrepared  Status = "prepared" // temp product + hash recorded, not committed
	StatusCommitted Status = "committed"
	StatusIsolated  Status = "isolated"
)

// Artifact is one published (or in-flight) fragment product.
type Artifact struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	IntervalIndex    int    `json:"interval_index"`
	RateIndex        int    `json:"rate_index"`
	RateName         string `json:"rate_name"`
	RelPath          string `json:"rel_path"`
	TempRelPath      string `json:"temp_rel_path,omitempty"`
	Length           int64  `json:"length"`
	SHA256           string `json:"sha256"`
	TempSHA256       string `json:"temp_sha256,omitempty"`
	SourceStart      int64  `json:"source_start"`
	SourceEnd        int64  `json:"source_end"`
	StructuralDigest string `json:"structural_digest"`
	StartTick        uint64 `json:"start_tick"`
	EndTick          uint64 `json:"end_tick"`
	KeyframeOffset   int64  `json:"keyframe_offset"`
	Status           Status `json:"status"`
	Published        bool   `json:"published"`
	Attempts         int    `json:"attempts"`
	MappingPaused    bool   `json:"mapping_paused,omitempty"`
	MappingRecovered bool   `json:"mapping_recovered,omitempty"`
}

// IntervalRecord persists per-interval packaging state.
type IntervalRecord struct {
	Index            int    `json:"index"`
	ID               string `json:"id"`
	KeyframeOffset   int64  `json:"keyframe_offset"`
	StartOffset      int64  `json:"start_offset"`
	EndOffset        int64  `json:"end_offset"`
	StartTick        uint64 `json:"start_tick"`
	EndTick          uint64 `json:"end_tick"`
	StructuralDigest string `json:"structural_digest"`
	MappingPaused    bool   `json:"mapping_paused"`
	MappingRecovered bool   `json:"mapping_recovered"`
	PauseReason      string `json:"pause_reason,omitempty"`
}

// Stats are recovery/publication counters printed by the CLI.
type Stats struct {
	Recovered         int `json:"recovered"`
	DuplicatePublish  int `json:"duplicate_publish"`
	MissingArtifact   int `json:"missing_artifact"`
	Rebuilt           int `json:"rebuilt"`
	AdoptedTemp       int `json:"adopted_temp"`
	AlreadyConsistent int `json:"already_consistent"`
}

// Manifest is the versioned ledger root.
type Manifest struct {
	FormatVersion int              `json:"format_version"`
	SourceRelPath string           `json:"source_rel_path"`
	SourceSHA256  string           `json:"source_sha256"`
	SourceLength  int64            `json:"source_length"`
	Rates         int              `json:"rates"`
	RateNames     []string         `json:"rate_names"`
	FullPackets   int              `json:"full_packets"`
	Intervals     []IntervalRecord `json:"intervals"`
	Artifacts     []Artifact       `json:"artifacts"`
	Issues        []merr.Issue     `json:"issues"`

	RoundtripOK          int   `json:"roundtrip_ok"`
	RoundtripTotal       int   `json:"roundtrip_total"`
	KeyframeHits         int   `json:"keyframe_hits"`
	KeyframeTotal        int   `json:"keyframe_total"`
	BoundaryErrorPackets int   `json:"boundary_error_packets"`
	FanInAligned         bool  `json:"fanin_aligned"`
	Stats                Stats `json:"stats"`
}

// New returns an initialized manifest at the current format version.
func New(sourceRel string, rates int, rateNames []string) *Manifest {
	return &Manifest{FormatVersion: FormatVersion, SourceRelPath: sourceRel,
		Rates: rates, RateNames: rateNames, FanInAligned: true}
}

// FindArtifact returns a pointer to the artifact with id, or nil.
func (m *Manifest) FindArtifact(id string) *Artifact {
	for i := range m.Artifacts {
		if m.Artifacts[i].ID == id {
			return &m.Artifacts[i]
		}
	}
	return nil
}

// Upsert inserts or replaces an artifact by ID.
func (m *Manifest) UpsertArtifact(a Artifact) {
	for i := range m.Artifacts {
		if m.Artifacts[i].ID == a.ID {
			m.Artifacts[i] = a
			return
		}
	}
	m.Artifacts = append(m.Artifacts, a)
}

// Resolve safely joins a ledger-relative path under workDir.
func Resolve(workDir, rel string) (string, error) {
	return safepath.JoinUnder(workDir, filepath.FromSlash(rel))
}

// Save atomically writes the manifest (temp file + rename).
func (m *Manifest) Save(workDir string) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return err
	}
	final, err := safepath.JoinUnder(workDir, ManifestName)
	if err != nil {
		return err
	}
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Load reads and validates the manifest format version.
func Load(workDir string) (*Manifest, error) {
	p, err := safepath.JoinUnder(workDir, ManifestName)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.FormatVersion != FormatVersion {
		return nil, merr.New(merr.CatPathRejected, 0,
			"unsupported manifest format_version=%d (want %d)", m.FormatVersion, FormatVersion)
	}
	return &m, nil
}

// HashBytes returns the hex SHA-256 and length of b.
func HashBytes(b []byte) (string, int64) {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), int64(len(b))
}

// HashFile streams a file and returns hex SHA-256 plus length.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
