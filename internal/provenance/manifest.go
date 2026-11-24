package provenance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"archive-recovery/internal/media"
)

// FrameRecord links one indexed frame to its input range and timestamps.
type FrameRecord struct {
	FrameSeq     int    `json:"frame_seq"`
	Offset       int64  `json:"offset"`
	Length       int64  `json:"length"`
	IsKeyframe   bool   `json:"is_keyframe"`
	RawPTS       int64  `json:"raw_pts"`
	HasRawPTS    bool   `json:"has_raw_pts"`
	RepairedPTS  int64  `json:"repaired_pts"`
	RepairReason string `json:"repair_reason"`
	ChunkIndex   int    `json:"chunk_index"`
}

// ErrorRecord mirrors media.UnitError for the audit trail.
type ErrorRecord struct {
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
	Class  string `json:"class"`
	Reason string `json:"reason"`
}

// Manifest is the complete, deterministic audit document.
type Manifest struct {
	SchemaVersion  string  `json:"schema_version"`
	Codec          string  `json:"codec"`
	AdapterVersion string  `json:"adapter_version"`
	InputSHA256    string  `json:"input_sha256"`
	StreamSize     int64   `json:"stream_size"`
	ChunkBytes     int64   `json:"chunk_bytes"`
	Chunks         []Chunk `json:"chunks"`
	// ConfirmedChunks are chunk indices whose every touched unit parsed
	// completely; pending/deferred chunks cannot be claimed as verified.
	ConfirmedChunks []int         `json:"confirmed_chunks"`
	Frames          []FrameRecord `json:"frames"`
	Errors          []ErrorRecord `json:"errors"`
}

// Builder accumulates manifest facts while a job runs.
type Builder struct {
	m          Manifest
	chunkBytes int64
	confirmed  map[int]bool
}

// NewBuilder starts a manifest for one input.
func NewBuilder(codec, adapterVersion, inputSHA string, streamSize, chunkBytes int64) *Builder {
	return &Builder{
		chunkBytes: chunkBytes,
		confirmed:  map[int]bool{},
		m: Manifest{
			SchemaVersion:  "manifest-1",
			Codec:          codec,
			AdapterVersion: adapterVersion,
			InputSHA256:    inputSHA,
			StreamSize:     streamSize,
			ChunkBytes:     chunkBytes,
			Chunks:         []Chunk{},
			Frames:         []FrameRecord{},
			Errors:         []ErrorRecord{},
		},
	}
}

// AddChunks registers hashed input chunks.
func (b *Builder) AddChunks(chunks []Chunk) {
	b.m.Chunks = append(b.m.Chunks, chunks...)
}

// ConfirmUnit marks every chunk overlapped by [off, off+length) as
// verified. A chunk that hosts a truncated unit is never confirmed through
// this path, so deferred confirmation cannot fabricate coverage.
func (b *Builder) ConfirmUnit(off, length int64) {
	if b.chunkBytes <= 0 || length <= 0 {
		return
	}
	first := off / b.chunkBytes
	last := (off + length - 1) / b.chunkBytes
	for i := first; i <= last; i++ {
		b.confirmed[int(i)] = true
	}
}

// AddFrame appends one frame record and confirms the chunks it spans.
func (b *Builder) AddFrame(f FrameRecord) {
	b.m.Frames = append(b.m.Frames, f)
	b.ConfirmUnit(f.Offset, f.Length)
}

// AddError records a parse error; error regions stay unconfirmed unless a
// later complete unit independently covers the same chunks.
func (b *Builder) AddError(e media.UnitError) {
	b.m.Errors = append(b.m.Errors, ErrorRecord{
		Offset: e.Offset, Length: e.Length,
		Class: classString(e.Class), Reason: e.Reason,
	})
}

// Finalize freezes the confirmed-chunk set in deterministic order.
func (b *Builder) Finalize() Manifest {
	idx := make([]int, 0, len(b.confirmed))
	for i := range b.confirmed {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	b.m.ConfirmedChunks = idx
	sort.SliceStable(b.m.Frames, func(i, j int) bool { return b.m.Frames[i].Offset < b.m.Frames[j].Offset })
	return b.m
}

// Marshal renders canonical JSON: indentation fixed and map order irrelevant
// because every field is an ordered struct/slice.
func (m Manifest) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Digest returns the SHA-256 of the canonical serialization. Repeated runs
// over identical inputs produce the identical digest.
func (m Manifest) Digest() (string, error) {
	raw, err := m.Marshal()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func classString(c media.ErrorClass) string {
	switch c {
	case media.ClassInvalidArgument:
		return "invalid_argument"
	case media.ClassRecoverable:
		return "recoverable"
	case media.ClassCheckpoint:
		return "checkpoint"
	default:
		return "fatal"
	}
}
