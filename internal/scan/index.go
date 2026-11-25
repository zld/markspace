package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"

	"mediacore/internal/fault"
)

// IndexVersion is bumped when the on-disk index schema changes.
const IndexVersion = 1

// DefaultMaxEntries bounds sample and packet indexes.
const DefaultMaxEntries = 200_000

// Params are inputs that affect indexing or mapping. A change invalidates a
// cached index even when the source digest is unchanged.
type Params struct {
	TargetBitrate   int64   `json:"target_bitrate"`
	BandwidthBPS    []int64 `json:"bandwidth_bps,omitempty"`
	ReorderWindow   int     `json:"reorder_window"`
	MaxEntries      int     `json:"max_entries"`
	MappingVersion  int     `json:"mapping_version"`
	PreserveEdits   bool    `json:"preserve_edits"`
	OutputTimescale uint32  `json:"output_timescale"`
}

// DefaultParams returns deterministic acceptance-test parameters.
func DefaultParams() Params {
	return Params{
		TargetBitrate:   5_376_000,
		BandwidthBPS:    []int64{2_400_000, 9_600_000, 4_800_000},
		ReorderWindow:   64,
		MaxEntries:      DefaultMaxEntries,
		MappingVersion:  1,
		PreserveEdits:   true,
		OutputTimescale: 1_000_000_000,
	}
}

// EditMapping preserves an elst relationship in unified nanoseconds.
type EditMapping struct {
	SegmentDurationNS int64 `json:"segment_duration_ns"`
	MediaTimeNS       int64 `json:"media_time_ns"`
	MediaRate         int16 `json:"media_rate"`
}

// Track is codec, timing, and mapping metadata for one source track.
type Track struct {
	ID              uint32        `json:"id"`
	Handler         string        `json:"handler"` // vide or soun
	Kind            string        `json:"kind"`    // video or audio
	Codec           string        `json:"codec"`
	BoxOffset       int64         `json:"box_offset"`
	Timescale       uint32        `json:"timescale"`
	DurationUnits   uint64        `json:"duration_units"`
	DurationNS      int64         `json:"duration_ns"`
	Width           uint16        `json:"width,omitempty"`
	Height          uint16        `json:"height,omitempty"`
	Channels        uint16        `json:"channels,omitempty"`
	BitsPerSample   uint16        `json:"bits_per_sample,omitempty"`
	SampleRate      uint32        `json:"sample_rate,omitempty"` // Hz
	InitialOffsetNS int64         `json:"initial_offset_ns"`
	Edits           []EditMapping `json:"edits,omitempty"`
}

// TrackMapping is the auditable source-to-output track relationship.
type TrackMapping struct {
	SourceTrackID uint32 `json:"source_track_id"`
	OutputTrackID uint32 `json:"output_track_id"`
	SSRC          uint32 `json:"ssrc,omitempty"`
	SourceCodec   string `json:"source_codec,omitempty"`
	Codec         string `json:"codec"`
	Kind          string `json:"kind"`
	Action        string `json:"action"` // copy
	OffsetNS      int64  `json:"offset_ns"`
}

// SampleRef is a lossless sample boundary. Payload bytes remain in the source.
type SampleRef struct {
	TrackID       uint32 `json:"track_id"`
	Index         uint32 `json:"index"`
	Offset        int64  `json:"offset"`
	Size          uint32 `json:"size"`
	PTSUnits      int64  `json:"pts_units"`
	DTSUnits      int64  `json:"dts_units"`
	DurationUnits uint32 `json:"duration_units"`
	DurationNS    int64  `json:"duration_ns"`
	PTSNS         int64  `json:"pts_ns"`
	DTSNS         int64  `json:"dts_ns"`
	KeyFrame      bool   `json:"key_frame"`
	SHA256        string `json:"sha256,omitempty"`
	// Data is used only for in-memory recovered RTP payloads and is never
	// persisted in the index cache.
	Data []byte `json:"-"`
}

// PacketRef is one RTP packet in arrival order plus its fixture location.
type PacketRef struct {
	ArrivalIndex int    `json:"arrival_index"`
	RecordOffset int64  `json:"record_offset"`
	PacketOffset int64  `json:"packet_offset"`
	PayloadOff   int64  `json:"payload_offset"`
	Length       int    `json:"length"`
	SSRC         uint32 `json:"ssrc"`
	PayloadType  uint8  `json:"payload_type"`
	Sequence     uint16 `json:"sequence"`
	Timestamp    uint32 `json:"timestamp"`
	Marker       bool   `json:"marker"`
	TrackID      uint32 `json:"track_id"`
}

// RTPStreamMeta describes one SSRC clock and codec mapping.
type RTPStreamMeta struct {
	SSRC       uint32 `json:"ssrc"`
	TrackID    uint32 `json:"track_id"`
	Codec      string `json:"codec"`
	Kind       string `json:"kind"`
	Timescale  uint32 `json:"timescale"`
	Channels   uint16 `json:"channels,omitempty"`
	SampleRate uint32 `json:"sample_rate,omitempty"`
}

// Index is the deterministic input index and cache document.
type Index struct {
	Version      int             `json:"version"`
	SourcePath   string          `json:"source_path"`
	Format       Format          `json:"format"`
	SourceSize   int64           `json:"source_size"`
	SourceSHA256 string          `json:"source_sha256"`
	Params       Params          `json:"params"`
	Tracks       []Track         `json:"tracks"`
	Mapping      []TrackMapping  `json:"mapping,omitempty"`
	Samples      []SampleRef     `json:"samples,omitempty"`
	Packets      []PacketRef     `json:"packets,omitempty"`
	RTPStreams   []RTPStreamMeta `json:"rtp_streams,omitempty"`
}

// BuildIndex scans and indexes a supported source.
func BuildIndex(path string, p Params) (*Index, error) {
	if p.MaxEntries <= 0 {
		p.MaxEntries = DefaultMaxEntries
	}
	info, err := Probe(path)
	if err != nil {
		return nil, err
	}
	digest, err := SHA256File(path)
	if err != nil {
		return nil, err
	}
	idx := &Index{
		Version:      IndexVersion,
		SourcePath:   path,
		Format:       info.Format,
		SourceSize:   info.Size,
		SourceSHA256: digest,
		Params:       p,
	}
	switch info.Format {
	case FormatMP4, FormatMOV:
		if err := buildMP4Index(idx); err != nil {
			return nil, err
		}
	case FormatRTP:
		if err := buildRTPIndex(idx); err != nil {
			return nil, err
		}
	default:
		return nil, fault.New(fault.UnsupportedFormat, string(info.Format))
	}
	if len(idx.Samples) > p.MaxEntries || len(idx.Packets) > p.MaxEntries {
		return nil, fault.New(fault.LimitExceeded, "index entry limit exceeded")
	}
	return idx, nil
}

// SaveCache atomically writes the index cache beside the requested cache path.
func SaveCache(cachePath string, idx *Index) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), cachePath, -1)
	}
	tmp := cachePath + ".tmp"
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fault.Wrap(fault.InvalidArgument, err)
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), tmp, -1)
	}
	if err := os.Rename(tmp, cachePath); err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), cachePath, -1)
	}
	return nil
}

// LoadCache reads a cache document.
func LoadCache(cachePath string) (*Index, error) {
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, fault.At(fault.Wrap(fault.NotFound, err), cachePath, -1)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fault.At(fault.Wrap(fault.CorruptHeader, err), cachePath, 0)
	}
	return &idx, nil
}

// LoadOrBuild reuses cache only when source digest and parameters match. The
// boolean reports whether the index was rebuilt.
func LoadOrBuild(source, cachePath string, p Params) (*Index, bool, error) {
	digest, err := SHA256File(source)
	if err != nil {
		return nil, false, err
	}
	if cached, err := LoadCache(cachePath); err == nil {
		if cached.Version == IndexVersion && cached.SourceSHA256 == digest && reflect.DeepEqual(cached.Params, p) {
			// Refresh the path in case the work directory was moved, but do
			// not mutate the cache merely for an absolute-path difference.
			cached.SourcePath = source
			return cached, false, nil
		}
	} else if !fault.IsCode(err, fault.NotFound) && !fault.IsCode(err, fault.CorruptHeader) {
		return nil, false, err
	}
	idx, err := BuildIndex(source, p)
	if err != nil {
		return nil, false, err
	}
	if err := SaveCache(cachePath, idx); err != nil {
		return nil, false, err
	}
	return idx, true, nil
}
