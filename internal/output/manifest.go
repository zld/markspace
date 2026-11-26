package output

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ManifestVersion is bumped when the on-disk schema changes incompatibly.
const ManifestVersion = 1

// InputSummary captures the identity and structure of the source container.
type InputSummary struct {
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	SizeBytes      int64  `json:"size_bytes"`
	DocType        string `json:"doc_type"`
	TimecodeScale  uint64 `json:"timecode_scale"`
	TrackCount     int    `json:"track_count"`
	BlockCount     int    `json:"block_count"`
}

// BlockRecord is the per-block audit row: where it lived in the input, what
// event it maps to, and which output frames it became.
type BlockRecord struct {
	TrackNo          uint64 `json:"track_no"`
	ElemOffset       int64  `json:"elem_offset"`
	DataOffset       int64  `json:"data_offset"`
	DataSize         int64  `json:"data_size"`
	BlockTimeNS      int64  `json:"block_time_ns"`
	EventTimeNS      int64  `json:"event_time_ns"`
	EventLine        int    `json:"event_line"`
	TimeErrorNS      int64  `json:"time_error_ns"`
	OutputFrameStart int64  `json:"output_frame_start"`
	OutputFrameEnd   int64  `json:"output_frame_end"`
	Gap              string `json:"gap,omitempty"`
	GapDetail        string `json:"gap_detail,omitempty"`
}

// TrackDelivery records the chosen AAC candidate and its measured quality.
type TrackDelivery struct {
	TrackNo          uint64  `json:"track_no"`
	CodecID          string  `json:"codec_id"`
	SourceRateHz     float64 `json:"source_rate_hz"`
	SourceChannels   uint64  `json:"source_channels"`
	OutputFile       string  `json:"output_file"`
	OutputSHA256     string  `json:"output_sha256"`
	OutputBytes      int64   `json:"output_bytes"`
	BitrateKbps      int     `json:"bitrate_kbps"`
	SampleCount      uint32  `json:"sample_count"`
	Timescale        uint32  `json:"timescale"`
	IntegratedLUFS   float64 `json:"integrated_lufs"`
	TruePeakDBTP     float64 `json:"true_peak_dbtp"`
	GatePassed       bool    `json:"gate_passed"`
	ByteDrift        int64   `json:"byte_drift"`
}

// RTPObservation is one RTP packet translated onto the shared NS clock.
type RTPObservation struct {
	SSRC        uint32 `json:"ssrc"`
	TrackNo     uint64 `json:"track_no"`
	Sequence    uint16 `json:"sequence"`
	RTPTS       uint32 `json:"rtp_ts"`
	ConvertedNS int64  `json:"converted_ns"`
	LoggedNS    int64  `json:"logged_ns"`
}

// FailureRecord pins a failure to the byte offset where it was detected.
type FailureRecord struct {
	Code   string `json:"code"`
	Offset int64  `json:"offset"`
	Detail string `json:"detail"`
}

// Manifest is the complete, deterministic audit document.
type Manifest struct {
	Version    int             `json:"version"`
	Input      InputSummary    `json:"input"`
	CacheState string          `json:"cache_state"`
	Blocks     []BlockRecord   `json:"blocks"`
	RTP        []RTPObservation `json:"rtp,omitempty"`
	Tracks     []TrackDelivery `json:"tracks"`
	Failures   []FailureRecord `json:"failures,omitempty"`
	Mapped     string          `json:"mapped"` // "4/4" style summary
	MaxErrorNS int64           `json:"max_time_error_ns"`
}

// Staging is an in-progress output directory. Its public directory is only
// populated on Publish; any error before that leaves no usable artifact.
type Staging struct {
	dir      string
	parent   string
	name     string
	manifest Manifest
}

// NewStaging creates a unique staging sibling of the final output directory.
func NewStaging(parent, name string) (*Staging, error) {
	dir, err := os.MkdirTemp(parent, "."+name+".staging-*")
	if err != nil {
		return nil, err
	}
	return &Staging{dir: dir, parent: parent, name: name, manifest: Manifest{Version: ManifestVersion}}, nil
}

// Dir returns the staging directory path.
func (s *Staging) Dir() string { return s.dir }

// SetManifest stores the manifest for publication.
func (s *Staging) SetManifest(m Manifest) { s.manifest = m }

// Publish writes the manifest, atomically swaps the staging directory into
// place and returns the final output path.
func (s *Staging) Publish() (string, error) {
	final := filepath.Join(s.parent, s.name)
	manifestBytes, err := json.MarshalIndent(s.manifest, "", "  ")
	if err != nil {
		return "", err
	}
	manifestBytes = append(manifestBytes, '\n')
	mPath := filepath.Join(s.dir, "manifest.json")
	if err := writeAtomic(mPath, manifestBytes); err != nil {
		return "", err
	}
	// Remove any previous output, then publish with a single rename.
	if _, err := os.Lstat(final); err == nil {
		if err := os.RemoveAll(final); err != nil {
			return "", err
		}
	}
	if err := os.Rename(s.dir, final); err != nil {
		return "", err
	}
	return final, nil
}

// Abort removes the staging directory; safe to call after Publish.
func (s *Staging) Abort() { _ = os.RemoveAll(s.dir) }

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// FileSHA256 returns the hex SHA-256 and size of a regular file.
func FileSHA256(path string) (string, int64, error) {
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

// LoadManifest reads and validates a published manifest.
func LoadManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest parse: %w", err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("manifest version %d unsupported", m.Version)
	}
	return &m, nil
}
