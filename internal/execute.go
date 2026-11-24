package medpipe

import (
	"encoding/json"
	"fmt"
	"os"
)

// CheckpointSchemaVersion marks the on-disk resume-state format.
const CheckpointSchemaVersion = 1

// Checkpoint is the durable resume state for one track. It is rewritten
// atomically after every confirmed chunk, so an interrupted run restarts at
// the nearest keyframe at or before the last committed ordinal.
type Checkpoint struct {
	SchemaVersion  int    `json:"schema_version"`
	Track          string `json:"track"`
	NextOrdinal    int    `json:"next_ordinal"`
	LastKeyOrdinal int    `json:"last_keyframe_ordinal"`
	ChunksDone     int    `json:"chunks_done"`
	BytesWritten   int64  `json:"bytes_written"`
}

// CheckpointFileName is the conventional resume sidecar name.
func CheckpointFileName(track string) string { return track + ".checkpoint.json" }

// SaveCheckpoint writes resume state atomically inside baseDir.
func SaveCheckpoint(baseDir string, cp Checkpoint) error {
	cp.SchemaVersion = CheckpointSchemaVersion
	name, perr := ResolveLocal(baseDir, CheckpointFileName(cp.Track))
	if perr != nil {
		return perr
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(name, data, 0o644)
}

// LoadCheckpoint reads resume state. A missing file is a clean start.
func LoadCheckpoint(baseDir, track string) (*Checkpoint, error) {
	name, perr := ResolveLocal(baseDir, CheckpointFileName(track))
	if perr != nil {
		return nil, perr
	}
	data, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("checkpoint %s corrupt: %w", name, err)
	}
	if cp.SchemaVersion != CheckpointSchemaVersion {
		return nil, fmt.Errorf("checkpoint %s schema %d, want %d", name, cp.SchemaVersion, CheckpointSchemaVersion)
	}
	return &cp, nil
}

// ClearCheckpoint removes resume state after a track completes successfully.
func ClearCheckpoint(baseDir, track string) error {
	name, perr := ResolveLocal(baseDir, CheckpointFileName(track))
	if perr != nil {
		return perr
	}
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ExecuteOptions configures one streaming execution pass.
type ExecuteOptions struct {
	Track     string
	ChunkSize int // frames processed per confirmed chunk
}

// Executor streams indexed frames in bounded chunks. Only one chunk's frames
// are resident at a time, and the checkpoint is committed before the next
// chunk starts, which bounds both memory and rework after interruption.
type Executor struct {
	baseDir string
	reader  *FrameReader
	idx     *TrackIndex
	opts    ExecuteOptions
	cp      Checkpoint
	b       *ErrorBuilder

	// accumulators
	frameCount   int
	keyCount     int
	bytesWritten int64
	qualitySum   int
	qualityMin   uint8
	qualityMax   uint8
	qualityN     int
	maxAlignUS   int64
}

// NewExecutor opens the capture for indexed random access and resumes from
// any existing checkpoint. The index digest must already have been verified
// against the capture by the caller.
func NewExecutor(baseDir, capturePath string, idx *TrackIndex, opts ExecuteOptions) (*Executor, error) {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 16
	}
	fr, err := NewFrameReader(capturePath, idx, opts.Track)
	if err != nil {
		return nil, err
	}
	e := &Executor{
		baseDir:    baseDir,
		reader:     fr,
		idx:        idx,
		opts:       opts,
		b:          NewErrorBuilder(opts.Track),
		qualityMin: 255,
	}
	cp, err := LoadCheckpoint(baseDir, opts.Track)
	if err != nil {
		return nil, err
	}
	if cp != nil {
		e.cp = *cp
	} else {
		e.cp = Checkpoint{Track: opts.Track, LastKeyOrdinal: -1}
	}
	return e, nil
}

// Run executes the whole track, honoring resume state. The visit callback
// receives one chunk at a time and returns the number of payload bytes the
// sink accepted (for bitrate accounting). A callback error aborts the run
// without advancing the checkpoint past the failing chunk.
func (e *Executor) Run(visit func(chunk []Frame) (int64, error)) error {
	if e.idx.FrameCount == 0 {
		return ClearCheckpoint(e.baseDir, e.opts.Track)
	}
	start := e.resumeOrdinal()
	if err := e.reader.SeekFrame(start); err != nil {
		return err
	}
	for start < e.idx.FrameCount {
		end := start + e.opts.ChunkSize
		if end > e.idx.FrameCount {
			end = e.idx.FrameCount
		}
		chunk, err := e.reader.ReadChunk(end - start)
		if err != nil {
			return err
		}
		if len(chunk) == 0 {
			break
		}
		n, err := visit(chunk)
		if err != nil {
			return err
		}
		e.account(chunk, n)
		// Commit progress only after the chunk sink succeeds.
		e.cp.NextOrdinal = end
		e.cp.ChunksDone++
		e.cp.BytesWritten += n
		if lastKey := e.lastKeyInChunk(chunk); lastKey >= 0 {
			e.cp.LastKeyOrdinal = lastKey
		}
		if err := SaveCheckpoint(e.baseDir, e.cp); err != nil {
			return err
		}
		start = end
	}
	return ClearCheckpoint(e.baseDir, e.opts.Track)
}

// resumeOrdinal maps the committed checkpoint onto the nearest keyframe at or
// before the next unprocessed ordinal, guaranteeing decodable restart.
func (e *Executor) resumeOrdinal() int {
	if e.cp.NextOrdinal <= 0 {
		return 0
	}
	kf, ok := e.idx.KeyframeAtOrBefore(e.cp.NextOrdinal - 1)
	if !ok {
		return 0
	}
	return kf.Ordinal
}

func (e *Executor) lastKeyInChunk(chunk []Frame) int {
	last := -1
	for _, f := range chunk {
		if f.Keyframe {
			last = f.Ordinal
		}
	}
	return last
}

func (e *Executor) account(chunk []Frame, written int64) {
	for _, f := range chunk {
		e.frameCount++
		if f.Keyframe {
			e.keyCount++
		}
		if f.HasMeta && f.Meta.Quality > 0 {
			q := int(f.Meta.Quality)
			e.qualitySum += q
			e.qualityN++
			if f.Meta.Quality < e.qualityMin {
				e.qualityMin = f.Meta.Quality
			}
			if f.Meta.Quality > e.qualityMax {
				e.qualityMax = f.Meta.Quality
			}
		}
	}
	e.bytesWritten += written
}

// Close releases the capture file handle.
func (e *Executor) Close() error { return e.reader.Close() }

// Snapshot returns the current progress counters without exposing internals.
func (e *Executor) Snapshot() (frameCount, keyCount int, bytesWritten int64, cp Checkpoint) {
	return e.frameCount, e.keyCount, e.bytesWritten, e.cp
}

// ExecStats is the immutable per-track execution summary used by the manifest.
type ExecStats struct {
	FrameCount  int     `json:"frame_count"`
	KeyCount    int     `json:"keyframe_count"`
	Bytes       int64   `json:"bytes_written"`
	QualityMean float64 `json:"quality_mean"`
	QualityMin  uint8   `json:"quality_min"`
	QualityMax  uint8   `json:"quality_max"`
	MaxAlignUS  int64   `json:"max_alignment_error_us"`
}

// Stats finalizes accumulated counters. Alignment error comes from the
// index's clock statistics, measured on the ideal monotonic frame grid.
func (e *Executor) Stats() ExecStats {
	st := ExecStats{
		FrameCount: e.frameCount,
		KeyCount:   e.keyCount,
		Bytes:      e.bytesWritten,
		QualityMin: e.qualityMin,
		QualityMax: e.qualityMax,
		MaxAlignUS: e.idx.ClockStats.MaxErrorUS,
	}
	if e.qualityN > 0 {
		st.QualityMean = float64(e.qualitySum) / float64(e.qualityN)
	}
	if e.qualityN == 0 {
		st.QualityMin = 0
	}
	return st
}
