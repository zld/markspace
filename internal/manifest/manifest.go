// Package manifest records auditable inputs, outputs, gaps, and metrics.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/scan"
)

// SchemaVersion is the manifest document version.
const SchemaVersion = 1

// FileDigest identifies an input or output artifact.
type FileDigest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// IndexRecord records cache reuse/rebuild behavior.
type IndexRecord struct {
	CachePath      string `json:"cache_path"`
	SourceSHA256   string `json:"source_sha256"`
	IndexRebuilt   int    `json:"index_rebuilt"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
}

// TrackRecord is the persisted track mapping summary.
type TrackRecord struct {
	SourceTrackID uint32 `json:"source_track_id"`
	OutputTrackID uint32 `json:"output_track_id"`
	SSRC          uint32 `json:"ssrc,omitempty"`
	Kind          string `json:"kind"`
	Codec         string `json:"codec"`
	Timescale     uint32 `json:"timescale"`
	Channels      uint16 `json:"channels,omitempty"`
	SampleRate    uint32 `json:"sample_rate,omitempty"`
	OffsetNS      int64  `json:"offset_ns"`
	Action        string `json:"action"`
}

// TimeRange is one track's recovered media interval.
type TimeRange struct {
	TrackID uint32  `json:"track_id"`
	StartNS int64   `json:"start_ns"`
	EndNS   int64   `json:"end_ns"`
	StartMS float64 `json:"start_ms"`
	EndMS   float64 `json:"end_ms"`
	Samples int     `json:"samples"`
}

// RecoveryCounts contains RTP/index recovery counters.
type RecoveryCounts struct {
	DuplicatesRemoved int `json:"duplicates_removed"`
	ReorderedPackets  int `json:"reordered_packets"`
	MissingPackets    int `json:"missing_packets"`
	LateRejected      int `json:"late_rejected"`
	IndexRebuilt      int `json:"index_rebuilt"`
}

// Metrics contains terminal-review quantitative results.
type Metrics struct {
	TargetBitrate       int64   `json:"target_bitrate"`
	ActualBitrate       float64 `json:"actual_bitrate"`
	BitrateDeltaPct     float64 `json:"bitrate_delta_pct"`
	VideoPSNR           string  `json:"video_psnr"`
	VideoPSNRValue      float64 `json:"-"`
	AudioDiffSamples    int64   `json:"audio_diff_samples"`
	ManifestAlignmentMS float64 `json:"manifest_alignment_ms"`
}

// Manifest is the stable audit document.
type Manifest struct {
	SchemaVersion int            `json:"schema_version"`
	Case          string         `json:"case"`
	OK            bool           `json:"ok"`
	Input         FileDigest     `json:"input"`
	Output        FileDigest     `json:"output"`
	Index         IndexRecord    `json:"index"`
	Tracks        []TrackRecord  `json:"tracks"`
	TimeRanges    []TimeRange    `json:"time_ranges"`
	Gaps          []clock.Gap    `json:"gaps"`
	Recovery      RecoveryCounts `json:"recovery"`
	Metrics       Metrics        `json:"metrics"`
	Assertions    []string       `json:"assertions,omitempty"`
	Errors        []string       `json:"errors,omitempty"`
}

// New creates a manifest with schema version populated.
func New(caseName string) *Manifest {
	return &Manifest{SchemaVersion: SchemaVersion, Case: caseName, OK: true}
}

// AddError records a failure and marks the manifest not OK.
func (m *Manifest) AddError(err error) {
	m.OK = false
	if err != nil {
		m.Errors = append(m.Errors, err.Error())
	}
}

// BuildTracks copies index and mapping into audit records.
func BuildTracks(idx *scan.Index, mappings []scan.TrackMapping) []TrackRecord {
	trackByID := map[uint32]scan.Track{}
	for _, t := range idx.Tracks {
		trackByID[t.ID] = t
	}
	out := make([]TrackRecord, 0, len(mappings))
	for _, mp := range mappings {
		tr := trackByID[mp.SourceTrackID]
		out = append(out, TrackRecord{
			SourceTrackID: mp.SourceTrackID, OutputTrackID: mp.OutputTrackID, SSRC: mp.SSRC,
			Kind: mp.Kind, Codec: mp.Codec, Timescale: tr.Timescale,
			Channels: tr.Channels, SampleRate: tr.SampleRate, OffsetNS: mp.OffsetNS, Action: mp.Action,
		})
	}
	return out
}

// BuildTimeRanges computes per-track first/last sample ranges.
func BuildTimeRanges(samples []scan.SampleRef) []TimeRange {
	type acc struct {
		start, end int64
		n          int
	}
	byTrack := map[uint32]*acc{}
	var ids []uint32
	for _, s := range samples {
		a := byTrack[s.TrackID]
		if a == nil {
			a = &acc{start: s.PTSNS, end: s.PTSNS + durationNS(s)}
			byTrack[s.TrackID] = a
			ids = append(ids, s.TrackID)
		}
		if s.PTSNS < a.start {
			a.start = s.PTSNS
		}
		if end := s.PTSNS + durationNS(s); end > a.end {
			a.end = end
		}
		a.n++
	}
	out := make([]TimeRange, 0, len(ids))
	for _, id := range ids {
		a := byTrack[id]
		out = append(out, TimeRange{
			TrackID: id, StartNS: a.start, EndNS: a.end,
			StartMS: clock.DurationMS(a.start), EndMS: clock.DurationMS(a.end), Samples: a.n,
		})
	}
	return out
}

func durationNS(s scan.SampleRef) int64 {
	return s.DurationNS
}

// WriteJSON atomically writes the manifest.
func WriteJSON(path string, m *Manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fault.Wrap(fault.InvalidArgument, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
	}
	return os.Rename(tmp, path)
}

// CanonicalSHA256 hashes stable manifest JSON.
func CanonicalSHA256(m *Manifest) (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", fault.Wrap(fault.InvalidArgument, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
