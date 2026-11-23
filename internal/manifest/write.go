// Package manifest writes the stable, sortable consumption manifest.
//
// The document is plain JSON with fixed field order and no wall-clock fields,
// so identical inputs and parameters produce byte-identical output. A
// representation is one rung of the bitrate ladder and carries every track it
// packages. Segment digests are computed from the bytes actually written.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"mediatool/internal/index"
)

// Version is the manifest schema version.
const Version = 1

// TrackRep is one track's description inside a representation.
type TrackRep struct {
	TrackID     uint32           `json:"track_id"`
	Kind        string           `json:"kind"`
	Codec       string           `json:"codec"`
	Audio       *index.AudioInfo `json:"audio,omitempty"`
	Video       *index.VideoInfo `json:"video,omitempty"`
	ColorRecord string           `json:"color_record,omitempty"`
}

// Representation is one rung of the ladder, packaging all covered tracks.
type Representation struct {
	ID        string     `json:"id"`
	Bitrate   int        `json:"bitrate"`
	Bandwidth int        `json:"bandwidth"`
	Tracks    []TrackRep `json:"tracks"`
}

// SegmentRef points at the output written for one track of one boundary.
type SegmentRef struct {
	RepresentationID string `json:"representation_id"`
	TrackID          uint32 `json:"track_id"`
	Index            int    `json:"index"`
	StartMs          int64  `json:"start_ms"`
	DurationMs       int64  `json:"duration_ms"`
	KeyframeOffset   int64  `json:"keyframe_offset"`
	KeyframeSeq      uint16 `json:"keyframe_seq"`
	SkewMs           int64  `json:"skew_ms"`
	ByteLength       int64  `json:"byte_length"`
	// SHA256 covers exactly the bytes written for this segment.
	SHA256 string `json:"sha256"`
}

// Segment groups every representation's output for one shared boundary.
type Segment struct {
	Index      int          `json:"index"`
	StartMs    int64        `json:"start_ms"`
	DurationMs int64        `json:"duration_ms"`
	Refs       []SegmentRef `json:"refs"`
}

// Checkpoint summarizes the last committed boundary.
type Checkpoint struct {
	SegmentsCommitted  int    `json:"segments_committed"`
	LastEndMs          int64  `json:"last_end_ms"`
	LastRepresentation string `json:"last_representation"`
	InputDigest        string `json:"input_digest"`
	Resyncs            int    `json:"resyncs"`
	Checksum           string `json:"checksum"`
}

// Digest computes the checkpoint checksum with Checksum empty.
func (c Checkpoint) Digest() string {
	cp := c
	cp.Checksum = ""
	b, err := json.Marshal(&cp)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// UntrustedInterval mirrors the clock package without importing it here.
type UntrustedInterval struct {
	TrackID uint32 `json:"track_id"`
	FromNs  int64  `json:"from_ns"`
	ToNs    int64  `json:"to_ns"`
	Reason  string `json:"reason"`
	Offset  int64  `json:"offset"`
}

// FaultRef is a compact, located fault record.
type FaultRef struct {
	Kind     string `json:"kind"`
	TrackID  uint32 `json:"track_id,omitempty"`
	Offset   int64  `json:"offset"`
	Recovery string `json:"recovery"`
	Detail   string `json:"detail,omitempty"`
}

// Manifest is the top-level output document.
type Manifest struct {
	Version         int                 `json:"version"`
	InputDigest     string              `json:"input_digest"`
	InputBytes      int64               `json:"input_bytes"`
	Seed            int64               `json:"seed"`
	Tracks          int                 `json:"tracks"`
	Representations []Representation    `json:"representations"`
	Segments        []Segment           `json:"segments"`
	Checkpoint      Checkpoint          `json:"checkpoint"`
	AlignmentSkewMs int64               `json:"alignment_skew_ms"`
	Untrusted       []UntrustedInterval `json:"untrusted,omitempty"`
	Faults          []FaultRef          `json:"faults,omitempty"`
	Checksum        string              `json:"checksum"`
}

// New creates a manifest skeleton.
func New(inputDigest string, inputBytes, seed int64) *Manifest {
	return &Manifest{Version: Version, InputDigest: inputDigest, InputBytes: inputBytes, Seed: seed}
}

// Sort orders everything deterministically.
func (m *Manifest) Sort() {
	sort.SliceStable(m.Representations, func(i, j int) bool {
		if m.Representations[i].Bitrate != m.Representations[j].Bitrate {
			return m.Representations[i].Bitrate < m.Representations[j].Bitrate
		}
		return m.Representations[i].ID < m.Representations[j].ID
	})
	for i := range m.Representations {
		tr := m.Representations[i].Tracks
		sort.SliceStable(tr, func(a, b int) bool { return tr[a].TrackID < tr[b].TrackID })
	}
	sort.SliceStable(m.Segments, func(i, j int) bool {
		if m.Segments[i].Index != m.Segments[j].Index {
			return m.Segments[i].Index < m.Segments[j].Index
		}
		return m.Segments[i].StartMs < m.Segments[j].StartMs
	})
	for i := range m.Segments {
		refs := m.Segments[i].Refs
		sort.SliceStable(refs, func(a, b int) bool {
			if refs[a].TrackID != refs[b].TrackID {
				return refs[a].TrackID < refs[b].TrackID
			}
			return refs[a].RepresentationID < refs[b].RepresentationID
		})
	}
	sort.SliceStable(m.Faults, func(i, j int) bool {
		if m.Faults[i].Offset != m.Faults[j].Offset {
			return m.Faults[i].Offset < m.Faults[j].Offset
		}
		return m.Faults[i].Kind < m.Faults[j].Kind
	})
}

func (m *Manifest) canonicalJSON() ([]byte, error) {
	cp := *m
	cp.Checksum = ""
	return json.Marshal(&cp)
}

// Digest computes the manifest digest over canonical bytes.
func (m *Manifest) Digest() string {
	b, err := m.canonicalJSON()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Encode serializes with a recomputed checksum and trailing newline.
func (m *Manifest) Encode() ([]byte, error) {
	m.Sort()
	cp := *m
	cp.Checksum = cp.Digest()
	data, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return nil, err
	}
	m.Checksum = cp.Checksum
	return append(data, '\n'), nil
}

// Save writes the manifest atomically.
func (m *Manifest) Save(path string) error {
	data, err := m.Encode()
	if err != nil {
		return err
	}
	return WriteAtomic(path, data)
}

// WriteAtomic writes data to path via a same-directory temporary file.
func WriteAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".out-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
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
	return os.Rename(name, path)
}
