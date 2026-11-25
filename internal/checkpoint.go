package continuity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// A checkpoint captures the exact accepted-sample prefix so an interrupted run
// can resume. Restoring verifies the input digest, the track mapping hash and
// the committed output boundary; a mismatch stops recovery and names the first
// conflicting position instead of continuing on inconsistent state.

// CheckpointVersion is bumped whenever the on-disk schema changes.
const CheckpointVersion = 1

// maxCheckpointBytes bounds a checkpoint read.
const maxCheckpointBytes = 64 << 20

// CheckpointSample is the durable form of one accepted sample. Payload carries
// the compressed bytes verbatim so a restore reproduces identical output.
type CheckpointSample struct {
	Source     string `json:"source"`
	SourceID   uint32 `json:"source_id"`
	Target     string `json:"target"`
	TrackKey   string `json:"track_key"`
	DTS        int64  `json:"dts"`
	PTS        int64  `json:"pts"`
	Duration   int64  `json:"duration"`
	Sync       bool   `json:"sync"`
	Segment    string `json:"segment"`
	Index      int    `json:"index"`
	ByteOffset int64  `json:"byte_offset"`
	ByteLength int64  `json:"byte_length"`
	Payload    []byte `json:"payload"`
}

// Checkpoint is the persisted processing state.
type Checkpoint struct {
	Version        int                `json:"version"`
	RunID          string             `json:"run_id"`
	TrackMapHash   string             `json:"track_map_hash"`
	Watermark      int64              `json:"watermark"`
	CommittedEnd   int64              `json:"committed_end"`
	OutputDigest   string             `json:"output_digest"`
	Samples        []CheckpointSample `json:"samples"`
	LateWindow     int64              `json:"late_window"`
	LateRecomputed int                `json:"late_recomputed"`
	LateRejected   int                `json:"late_rejected"`
	// InputDigest covers the accepted samples as replayed inputs.
	InputDigest string `json:"input_digest"`
}

// CheckpointConfig identifies the invariants a checkpoint belongs to.
type CheckpointConfig struct {
	RunID        string
	TrackMapHash string
}

// NewCheckpoint snapshots the timeline's accepted prefix.
func NewCheckpoint(cfg CheckpointConfig, tl *Timeline) (*Checkpoint, error) {
	if tl == nil {
		return nil, Usagef("checkpoint requires a timeline")
	}
	prog, err := tl.Recompute()
	if err != nil {
		return nil, err
	}
	cp := &Checkpoint{
		Version:        CheckpointVersion,
		RunID:          cfg.RunID,
		TrackMapHash:   cfg.TrackMapHash,
		Watermark:      int64(tl.Watermark()),
		CommittedEnd:   int64(prog.CommittedEnd),
		LateWindow:     int64(tl.cfg.LateWindow),
		LateRecomputed: prog.Stats.LateRecomputed,
		LateRejected:   prog.Stats.LateRejected,
	}
	for _, s := range tl.accepted {
		cp.Samples = append(cp.Samples, CheckpointSample{
			Source: s.Source, SourceID: s.SourceID, Target: s.Target, TrackKey: s.TrackKey,
			DTS: int64(s.DTS), PTS: int64(s.PTS), Duration: int64(s.Duration), Sync: s.Sync,
			Segment: s.Segment, Index: s.Index,
			ByteOffset: s.ByteOffset, ByteLength: s.ByteLength,
			Payload: append([]byte(nil), s.Data...),
		})
	}
	cp.OutputDigest = OutputDigest(cp.Samples)
	cp.InputDigest = InputDigest(acceptedToSamples(tl.accepted))
	return cp, nil
}

// InputDigest hashes the ordered identities and payloads of a sample stream.
func InputDigest(samples []Sample) string {
	h := sha256.New()
	h.Write([]byte("continuity/input/v1\n"))
	for _, s := range samples {
		fmt.Fprintf(h, "%s|%s|%d|%d|%d|", s.TrackKey, s.Segment, s.Index, s.DTS, s.PTS)
		h.Write(s.Data)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// OutputDigest hashes the committed output samples (identity, timing, bytes).
func OutputDigest(samples []CheckpointSample) string {
	h := sha256.New()
	h.Write([]byte("continuity/output/v1\n"))
	for _, s := range samples {
		fmt.Fprintf(h, "%s|%s|%d|%d|%d|%t|", s.Source, s.Target, s.DTS, s.PTS, s.Duration, s.Sync)
		h.Write(s.Payload)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// MarshalCheckpoint serialises cp as deterministic JSON.
func MarshalCheckpoint(cp *Checkpoint) ([]byte, error) {
	if cp == nil {
		return nil, Usagef("nil checkpoint")
	}
	return json.MarshalIndent(cp, "", "  ")
}

// LoadCheckpoint reads and validates the checkpoint file at path.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	display := filepath.ToSlash(path)
	info, err := os.Stat(path)
	if err != nil {
		return nil, WrapError(err, KindCheckpoint, Location{File: display, Offset: -1, Sample: -1}, "checkpoint not readable")
	}
	if info.Size() > maxCheckpointBytes {
		return nil, NewError(KindCheckpoint, Location{File: display, Offset: -1, Sample: -1},
			"checkpoint is %d bytes, limit %d", info.Size(), maxCheckpointBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, WrapError(err, KindCheckpoint, Location{File: display, Offset: -1, Sample: -1}, "checkpoint not readable")
	}
	var cp Checkpoint
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cp); err != nil {
		return nil, WrapError(err, KindCheckpoint, Location{File: display, Offset: -1, Sample: -1}, "checkpoint is malformed")
	}
	if cp.Version != CheckpointVersion {
		return nil, NewError(KindCheckpoint, Location{File: display, Offset: -1, Sample: -1},
			"checkpoint version %d is not supported (want %d)", cp.Version, CheckpointVersion)
	}
	if got := OutputDigest(cp.Samples); got != cp.OutputDigest {
		return nil, NewError(KindCheckpoint, Location{File: display, Offset: -1, Sample: -1},
			"checkpoint output digest mismatch: recorded %s, recomputed %s", cp.OutputDigest, got)
	}
	return &cp, nil
}

// SaveCheckpoint writes cp atomically via a temporary file and rename.
func SaveCheckpoint(path string, cp *Checkpoint) error {
	data, err := MarshalCheckpoint(cp)
	if err != nil {
		return err
	}
	display := filepath.ToSlash(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return WrapError(err, KindCheckpoint, Location{File: display, Offset: -1, Sample: -1}, "cannot create checkpoint directory")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return WrapError(err, KindCheckpoint, Location{File: display, Offset: -1, Sample: -1}, "cannot write checkpoint")
	}
	if err := os.Rename(tmp, path); err != nil {
		return WrapError(err, KindCheckpoint, Location{File: display, Offset: -1, Sample: -1}, "cannot commit checkpoint")
	}
	return nil
}

// RestoreCheckpoint validates a checkpoint against a fresh run's invariants and
// rebuilds the timeline. The track mapping hash, the committed output boundary,
// the watermark and the output digest must all agree; the first sample that
// cannot be replayed is reported with its position.
func RestoreCheckpoint(cp *Checkpoint, tm *TrackMap, cfg TimelineConfig) (*Timeline, error) {
	if cp == nil {
		return nil, Usagef("nil checkpoint")
	}
	if tm == nil {
		return nil, Usagef("checkpoint restore requires a track map")
	}
	if cp.TrackMapHash != tm.Hash() {
		return nil, NewError(KindCheckpoint, Location{Offset: -1, Sample: -1},
			"track mapping changed since checkpoint: recorded %s, current %s", cp.TrackMapHash, tm.Hash())
	}
	tl, err := NewTimeline(tm, cfg)
	if err != nil {
		return nil, err
	}
	if int64(tl.cfg.LateWindow) != cp.LateWindow {
		// The window shapes which samples were accepted; a different window
		// would replay into different state.
		return nil, NewError(KindCheckpoint, Location{Offset: -1, Sample: -1},
			"late window changed since checkpoint: recorded %d ticks, current %d", cp.LateWindow, tl.cfg.LateWindow)
	}
	for i, cs := range cp.Samples {
		if _, ok := tm.Lookup(cs.Source, cs.SourceID); !ok {
			return nil, NewError(KindCheckpoint, Location{File: cs.Segment, Offset: cs.ByteOffset, Sample: cs.Index},
				"checkpoint sample %d references %s track %d, which is no longer mapped", i, cs.Source, cs.SourceID)
		}
		s := Sample{
			TrackKey: cs.TrackKey, SourceID: cs.SourceID,
			Segment: cs.Segment, File: cs.Segment, Index: cs.Index,
			DTS: Ticks(cs.DTS), PTS: Ticks(cs.PTS), Duration: Ticks(cs.Duration), Sync: cs.Sync,
			ByteOffset: cs.ByteOffset, ByteLength: cs.ByteLength, Data: cs.Payload,
		}
		if err := tl.Add(cs.Source, s); err != nil {
			return nil, WrapError(err, KindCheckpoint, Location{File: cs.Segment, Offset: cs.ByteOffset, Sample: cs.Index},
				"checkpoint sample %d cannot be replayed", i)
		}
	}
	if int64(tl.Watermark()) != cp.Watermark {
		return nil, NewError(KindCheckpoint, Location{Offset: -1, Sample: -1},
			"restored watermark %d does not match checkpoint %d", tl.Watermark(), cp.Watermark)
	}
	prog, err := tl.Recompute()
	if err != nil {
		return nil, err
	}
	if int64(prog.CommittedEnd) != cp.CommittedEnd {
		return nil, NewError(KindCheckpoint, Location{Offset: -1, Sample: -1},
			"restored committed boundary %d does not match checkpoint %d", prog.CommittedEnd, cp.CommittedEnd)
	}
	replayed := acceptedToSamples(tl.accepted)
	if got := InputDigest(replayed); got != cp.InputDigest {
		for i, cs := range cp.Samples {
			if i >= len(replayed) || replayed[i].Index != cs.Index || replayed[i].Segment != cs.Segment {
				return nil, NewError(KindCheckpoint, Location{File: cs.Segment, Offset: cs.ByteOffset, Sample: cs.Index},
					"input digest mismatch: first conflicting sample is checkpoint sample %d", i)
			}
		}
		return nil, NewError(KindCheckpoint, Location{Offset: -1, Sample: -1},
			"restored input digest %s does not match checkpoint %s", got, cp.InputDigest)
	}
	if got := OutputDigest(samplesToCheckpoint(tl.accepted)); got != cp.OutputDigest {
		return nil, NewError(KindCheckpoint, Location{Offset: -1, Sample: -1},
			"restored output digest %s does not match checkpoint %s", got, cp.OutputDigest)
	}
	return tl, nil
}

// VerifyInputPrefix confirms that the first len(cp.Samples) entries of inputs
// reproduce the checkpoint's recorded input digest. It returns the index of the
// first conflicting entry when they differ.
func VerifyInputPrefix(cp *Checkpoint, inputs []Sample) (int, error) {
	if len(inputs) < len(cp.Samples) {
		return len(inputs), NewError(KindCheckpoint, Location{Offset: -1, Sample: -1},
			"only %d input samples available, checkpoint needs %d", len(inputs), len(cp.Samples))
	}
	prefix := inputs[:len(cp.Samples)]
	if got := InputDigest(prefix); got == cp.InputDigest {
		return -1, nil
	}
	for i := range cp.Samples {
		cs, in := cp.Samples[i], prefix[i]
		if cs.Segment != in.Segment || cs.Index != in.Index || cs.PTS != int64(in.PTS) ||
			cs.DTS != int64(in.DTS) || !bytesEqual(cs.Payload, in.Data) {
			return i, NewError(KindCheckpoint, Location{File: in.File, Segment: in.Segment, Offset: in.ByteOffset, Sample: in.Index},
				"input digest mismatch at sample %d", i)
		}
	}
	return 0, NewError(KindCheckpoint, Location{Offset: -1, Sample: -1}, "input digest mismatch")
}

func acceptedToSamples(in []PlacedSample) []Sample {
	out := make([]Sample, 0, len(in))
	for _, s := range in {
		out = append(out, Sample{
			TrackKey: s.TrackKey, SourceID: s.SourceID,
			Segment: s.Segment, File: s.Segment, Index: s.Index,
			DTS: s.DTS, PTS: s.PTS, Duration: s.Duration, Sync: s.Sync,
			ByteOffset: s.ByteOffset, ByteLength: s.ByteLength, Data: s.Data,
		})
	}
	return out
}

func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }

func samplesToCheckpoint(in []PlacedSample) []CheckpointSample {
	out := make([]CheckpointSample, 0, len(in))
	for _, s := range in {
		out = append(out, CheckpointSample{
			Source: s.Source, SourceID: s.SourceID, Target: s.Target, TrackKey: s.TrackKey,
			DTS: int64(s.DTS), PTS: int64(s.PTS), Duration: int64(s.Duration), Sync: s.Sync,
			Segment: s.Segment, Index: s.Index, ByteOffset: s.ByteOffset, ByteLength: s.ByteLength,
			Payload: s.Data,
		})
	}
	return out
}
