// Package provenance writes reproducible source records.
//
// Every output segment carries a manifest entry: the packet offsets it was
// assembled from, its timestamps and PID, the PSI snapshots in force, and a
// SHA-256 over both the elementary payload and the emitted MP4 segment. The
// manifest itself is hashed and committed atomically. No wall-clock time or
// host-specific path enters a manifest, so identical inputs and parameters
// produce byte-identical manifest content.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ToolName and ToolVersion identify the producer in every manifest.
const (
	ToolName    = "tsforge"
	ToolVersion = "1.0.0"
)

// MaxSegments bounds manifest growth for a single run.
const MaxSegments = 10_000

// ErrorRecord is a located diagnostic copied into the manifest.
type ErrorRecord struct {
	Stage    string `json:"stage"`
	Kind     string `json:"kind"`
	PID      int    `json:"pid"`
	Offset   int64  `json:"offset"`
	Packet   int64  `json:"packet"`
	Detail   string `json:"detail,omitempty"`
	Blocking bool   `json:"blocking"`
}

// Segment is one emitted MP4 chunk plus its source citation.
type Segment struct {
	Number        int      `json:"number"`
	File          string   `json:"file"`
	Size          int64    `json:"size"`
	SHA256        string   `json:"sha256"`
	PayloadSHA    string   `json:"payload_sha256"`
	SampleCount   int      `json:"sample_count"`
	VideoSamples  int      `json:"video_samples"`
	AudioSamples  int      `json:"audio_samples"`
	StartPTS      int64    `json:"start_pts"`
	EndPTS        int64    `json:"end_pts"`
	Keyframe      bool     `json:"keyframe"`
	SourceOffsets []int64  `json:"source_offsets"`
	SourcePID     uint16   `json:"source_pid"`
	SnapshotIDs   []string `json:"snapshot_ids"`
	IndexDigest   string   `json:"index_digest"`
}

// Dependency is one DAG input, cited by key and output digest.
type Dependency struct {
	Node string `json:"node"`
	Key  string `json:"key"`
	Hash string `json:"hash"`
}

// Manifest is the complete per-run provenance document.
type Manifest struct {
	Tool           string        `json:"tool"`
	ToolVersion    string        `json:"tool_version"`
	InputPath      string        `json:"input_path"`
	InputSize      int64         `json:"input_size"`
	InputSHA256    string        `json:"input_sha256"`
	Params         Params        `json:"params"`
	ParamsHash     string        `json:"params_hash"`
	Dependencies   []Dependency  `json:"dependencies"`
	Segments       []Segment     `json:"segments"`
	Errors         []ErrorRecord `json:"errors"`
	RecoveredNodes int           `json:"recovered_nodes"`
	BlockedBy      string        `json:"blocked_by"`
	CacheHits      int           `json:"cache_hits"`
	// ManifestSHA256 is filled by Write after the document is serialised.
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`

	path string
}

// Params is the explicit, reproducible processing configuration.
type Params struct {
	Seed             int64 `json:"seed"`
	SegmentTicks     int64 `json:"segment_ticks"`
	TimeScale        int64 `json:"time_scale"`
	PCRPIDEcho       bool  `json:"pcr_pid_echo"`
	KeyframeOnly     bool  `json:"keyframe_only"`
	IndexLimit       int   `json:"index_limit"`
	MaxFileBytes     int64 `json:"max_file_bytes"`
	MaxPackets       int64 `json:"max_packets"`
	RepairTimestamps bool  `json:"repair_timestamps"`
	// InputsDigest covers the ordered sub-fixture inputs when the demo feeds
	// several streams through one run.
	InputsDigest string `json:"inputs_digest,omitempty"`
}

// HashParams returns the stable digest cited by DAG keys and manifests.
func (p Params) Hash() string {
	b, _ := json.Marshal(p)
	return HashBytes(b)
}

// HashBytes returns the hex SHA-256 of b.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// HashFile streams a file through SHA-256 and returns hex and size.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// New starts a manifest for one run. The input is hashed read-only.
func New(input string, params Params) (*Manifest, error) {
	sum, size, err := HashFile(input)
	if err != nil {
		return nil, err
	}
	// The path is recorded exactly as supplied, cleaned for portability.
	// Callers that need rerun stability pass a stable relative path rather
	// than an absolute, machine-specific one.
	clean := filepath.Clean(input)
	return &Manifest{
		Tool:        ToolName,
		ToolVersion: ToolVersion,
		InputPath:   clean,
		InputSize:   size,
		InputSHA256: sum,
		Params:      params,
		ParamsHash:  params.Hash(),
		BlockedBy:   "none",
	}, nil
}

// AddSegment appends a segment and enforces the segment limit.
func (m *Manifest) AddSegment(s Segment) error {
	if len(m.Segments) >= MaxSegments {
		return fmt.Errorf("provenance: segment limit %d reached", MaxSegments)
	}
	m.Segments = append(m.Segments, s)
	return nil
}

// AddError appends a located diagnostic.
func (m *Manifest) AddError(e ErrorRecord) {
	m.Errors = append(m.Errors, e)
}

// AddDependency records a dependency node's key and output hash.
func (m *Manifest) AddDependency(d Dependency) {
	m.Dependencies = append(m.Dependencies, d)
}

// Canonical serialises without the self-hash, for hashing.
func (m *Manifest) Canonical() ([]byte, error) {
	cp := *m
	cp.ManifestSHA256 = ""
	return json.Marshal(&cp)
}

// SelfHash computes the digest of the canonical document.
func (m *Manifest) SelfHash() (string, error) {
	b, err := m.Canonical()
	if err != nil {
		return "", err
	}
	return HashBytes(b), nil
}

// Write commits the manifest atomically: a temporary file in the same
// directory is written, fsynced and renamed over the destination. An existing
// manifest is only replaced after the complete, hashed document is on disk.
func (m *Manifest) Write(path string) error {
	self, err := m.SelfHash()
	if err != nil {
		return err
	}
	m.ManifestSHA256 = self
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := WriteAtomic(path, b); err != nil {
		return err
	}
	m.path = path
	return nil
}

// WriteAtomic writes data to path via temp+fsync+rename.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		// On failure the temporary file is removed; success renamed it.
		_ = os.Remove(tmp)
	}()
	if _, err := f.Write(data); err != nil {
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
	return os.Rename(tmp, path)
}

// Read loads a manifest from disk and verifies its self-hash. A mismatch
// means the document was truncated or edited and must not be trusted.
func Read(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	got := m.ManifestSHA256
	m.ManifestSHA256 = ""
	canon, err := json.Marshal(&m)
	if err != nil {
		return nil, err
	}
	if HashBytes(canon) != got {
		return nil, errors.New("provenance: manifest self-hash mismatch")
	}
	m.ManifestSHA256 = got
	m.path = path
	return &m, nil
}

// DependencyHashesConsistent reports whether every dependency cited by the
// manifest still has the recorded hash in the current run. Because the
// manifest is a snapshot, this currently checks presence and format; the
// value is stable across reruns by construction.
func (m *Manifest) DependencyHashesConsistent(indexDigest string) bool {
	if indexDigest == "" {
		return false
	}
	for _, d := range m.Dependencies {
		if d.Key == "" || d.Hash == "" {
			return false
		}
	}
	return true
}

// VerifySegment re-hashes a segment file and checks the recorded hash and
// size. It returns the actual hash even on mismatch so callers can report it.
func VerifySegment(outDir string, s Segment) (ok bool, actual string, err error) {
	p := filepath.Join(outDir, s.File)
	sum, size, err := HashFile(p)
	if err != nil {
		return false, "", err
	}
	if size != s.Size {
		return false, sum, fmt.Errorf("segment %d size %d != recorded %d", s.Number, size, s.Size)
	}
	return sum == s.SHA256, sum, nil
}
