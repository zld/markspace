// Package provenance writes the reviewable manifest that ties every output
// segment back to the ingest bytes that produced it.
//
// The manifest is deliberately free of wall-clock timestamps and map iteration
// order: two runs over identical inputs and configuration must serialize to
// byte-identical JSON, otherwise idempotency claims cannot be checked. Every
// volatile fact is expressed in media ticks instead.
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
	"sort"

	"mediarun/internal/fault"
)

// Version is the manifest schema version.
const Version = 1

// Input describes one ingest artifact.
type Input struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// FaultRecord is the serializable form of fault.Error.
type FaultRecord struct {
	Stage       string `json:"stage"`
	Kind        string `json:"kind"`
	Track       uint8  `json:"track,omitempty"`
	Offset      int64  `json:"offset"`
	Record      int    `json:"record"`
	Detail      string `json:"detail,omitempty"`
	Recoverable bool   `json:"recoverable"`
}

// FromFault converts a typed fault into a record.
func FromFault(e *fault.Error) FaultRecord {
	return FaultRecord{
		Stage:       string(e.Stage),
		Kind:        string(e.Kind),
		Track:       e.Track,
		Offset:      e.Offset,
		Record:      e.Record,
		Detail:      e.Detail,
		Recoverable: e.Recoverable,
	}
}

// StageSummary is the checkpointable summary of one stage.
type StageSummary struct {
	Name       string         `json:"name"`
	InputHash  string         `json:"input_hash"`
	ConfigHash string         `json:"config_hash"`
	OutputHash string         `json:"output_hash"`
	Detail     string         `json:"detail,omitempty"`
	Counts     map[string]int `json:"counts,omitempty"`
	Faults     []FaultRecord  `json:"faults,omitempty"`
}

// Segment describes one remuxed output.
type Segment struct {
	Index        int           `json:"index"`
	Path         string        `json:"path"`
	Bytes        int64         `json:"bytes"`
	SHA256       string        `json:"sha256"`
	Samples      int           `json:"samples"`
	StartPTS     uint64        `json:"start_pts"`
	EndPTS       uint64        `json:"end_pts"`
	Keyframes    int           `json:"keyframes"`
	KeyframeHits int           `json:"keyframe_hits"`
	RoundtripHit int           `json:"roundtrip_hit"`
	Synced       bool          `json:"synced"`
	Incomplete   bool          `json:"incomplete"`
	AudioFrames  int           `json:"audio_frames"`
	AudioBytes   int           `json:"audio_bytes"`
	Faults       []FaultRecord `json:"faults,omitempty"`
}

// Metrics are the acceptance counters printed by the CLI.
type Metrics struct {
	Frames             int `json:"frames"`
	AudioPackets       int `json:"audio_packets"`
	ParseErrors        int `json:"parse_errors"`
	RecoveredAudio     int `json:"recovered_audio"`
	RecoveredVideo     int `json:"recovered_video"`
	DroppedAudio       int `json:"dropped_audio"`
	Segments           int `json:"segments"`
	KeyframeTotal      int `json:"keyframe_total"`
	KeyframeHits       int `json:"keyframe_hits"`
	RoundtripTotal     int `json:"roundtrip_total"`
	RoundtripHits      int `json:"roundtrip_hits"`
	IncompleteSegments int `json:"incomplete_segments"`
	UnsyncedSegments   int `json:"unsynced_segments"`
}

// Manifest is the full provenance document for one run.
type Manifest struct {
	Version     int            `json:"version"`
	Tool        string         `json:"tool"`
	ConfigHash  string         `json:"config_hash"`
	Timescale   uint32         `json:"timescale"`
	Inputs      []Input        `json:"inputs"`
	Stages      []StageSummary `json:"stages"`
	Segments    []Segment      `json:"segments"`
	Faults      []FaultRecord  `json:"faults"`
	Metrics     Metrics        `json:"metrics"`
	TimelineEnd int64          `json:"timeline_end"`
}

// HashBytesHex returns the hex SHA-256 of b.
func HashBytesHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// HashFile returns the digest and size of a file.
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

// CanonicalJSON marshals v deterministically. encoding/json emits struct
// fields in declaration order and sorts map keys, so no extra sorting is
// needed; this helper exists to centralize the choice and the trailing
// newline convention.
func CanonicalJSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// SortFaults orders fault records by offset, stage and kind for stable output.
// Records without a source offset sort last so byte-anchored faults lead.
func SortFaults(fs []FaultRecord) {
	sort.SliceStable(fs, func(i, j int) bool {
		ai, aj := fs[i].Offset, fs[j].Offset
		if (ai < 0) != (aj < 0) {
			return ai >= 0
		}
		if ai != aj {
			return ai < aj
		}
		if fs[i].Stage != fs[j].Stage {
			return fs[i].Stage < fs[j].Stage
		}
		return fs[i].Kind < fs[j].Kind
	})
}

// WriteAtomic serializes the manifest and replaces path atomically. A crash
// mid-write leaves either the previous manifest or the new one, never a
// truncated file.
func WriteAtomic(path string, m *Manifest) error {
	if m == nil {
		return fault.New(fault.StagePipeline, fault.KindConfig, "nil manifest")
	}
	SortFaults(m.Faults)
	for i := range m.Stages {
		SortFaults(m.Stages[i].Faults)
	}
	for i := range m.Segments {
		SortFaults(m.Segments[i].Faults)
	}
	data, err := CanonicalJSON(m)
	if err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "marshal manifest")
	}
	return WriteFileAtomic(path, data)
}

// WriteFileAtomic writes data to a sibling temporary file, fsyncs it and
// renames it over path.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "create %s", dir)
	}
	tmp, err := os.CreateTemp(dir, ".mediarun-*.tmp")
	if err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "temp file in %s", dir)
	}
	name := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(name)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "write %s", name)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "fsync %s", name)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "close %s", name)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "rename %s", path)
	}
	return nil
}

// Load reads a manifest from disk.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return &m, nil
}

// Validate performs structural checks used before trusting a checkpoint or a
// comparison baseline.
func (m *Manifest) Validate() error {
	if m == nil {
		return errors.New("nil manifest")
	}
	if m.Version != Version {
		return fmt.Errorf("manifest version %d, want %d", m.Version, Version)
	}
	if m.Timescale == 0 {
		return errors.New("manifest timescale is zero")
	}
	for i, s := range m.Segments {
		if s.Index != i {
			return fmt.Errorf("segment %d has index %d", i, s.Index)
		}
	}
	return nil
}

// SegmentHashes returns the per-segment digests in index order.
func (m *Manifest) SegmentHashes() []string {
	out := make([]string, len(m.Segments))
	for i, s := range m.Segments {
		out[i] = s.SHA256
	}
	return out
}

// EqualHashes compares two digest slices.
func EqualHashes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Summarize renders the acceptance line for terminal output.
func (m *Manifest) Summarize() string {
	parseErrors := m.Metrics.ParseErrors
	first := int64(-1)
	for _, f := range m.Faults {
		if f.Offset >= 0 && (first < 0 || f.Offset < first) {
			first = f.Offset
		}
	}
	return fmt.Sprintf("frames=%d parse_errors=%d first_error_offset=%d recovered_audio=%d segments=%d",
		m.Metrics.Frames, parseErrors, first, m.Metrics.RecoveredAudio, m.Metrics.Segments)
}
