// Package kernel wires the media stages together and drives the demo. It maps
// stage failures to the CLI's exit-code contract.
package kernel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"mediad/internal/ebml"
	"mediad/internal/index"
	"mediad/internal/pipeline"
	"mediad/internal/segment"
	"mediad/internal/timeline"
)

// ExitCode follows the acceptance contract.
type ExitCode int

const (
	ExitOK         ExitCode = 0
	ExitInternal   ExitCode = 1
	ExitInput      ExitCode = 2
	ExitProvenance ExitCode = 3
)

// StageError classifies a failure by stage.
type StageError struct {
	Stage string
	Code  ExitCode
	Err   error
}

func (e *StageError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }

// CodeOf maps an error to its exit class.
func CodeOf(err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	var se *StageError
	if errors.As(err, &se) {
		return se.Code
	}
	return ExitInternal
}

func inputErr(stage string, err error) error {
	return &StageError{Stage: stage, Code: ExitInput, Err: err}
}
func provErr(stage string, err error) error {
	return &StageError{Stage: stage, Code: ExitProvenance, Err: err}
}
func internalErr(stage string, err error) error {
	return &StageError{Stage: stage, Code: ExitInternal, Err: err}
}

// Config holds the tunables that affect output.
type Config struct {
	Seed            int64
	Representations int
	ResumeAfter     int
	DurationSec     int
	TargetDuration  int64
	Tolerance       int64
	FrameNanos      int64
	WorkDir         string
}

// DefaultConfig returns the demo defaults.
func DefaultConfig() Config {
	return Config{
		Seed:            42,
		Representations: 3,
		ResumeAfter:     2,
		DurationSec:     15,
		TargetDuration:  1_000_000_000,
		Tolerance:       33_333_333,
		FrameNanos:      33_333_333,
	}
}

// Representation is one parsed rendition.
type Representation struct {
	ID        string
	Bandwidth uint64
	Data      []byte
	Doc       *ebml.Doc
	Index     *index.Index
	Keyframes []int64
	Duration  int64
}

// SegmentConfig returns the manifest config for a parsed plan.
func (c Config) SegmentConfig(timecodeScale uint64) segment.Config {
	return segment.Config{
		Representations: c.Representations,
		TargetDuration:  c.TargetDuration,
		Tolerance:       c.Tolerance,
		TimecodeScale:   timecodeScale,
		FrameNanos:      c.FrameNanos,
	}
}

// digestBytes hex-encodes the SHA-256 of b.
func digestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ParseRepresentation parses one rendition and normalizes its timeline.
func ParseRepresentation(id string, bandwidth uint64, data []byte, frameNanos int64) (*Representation, error) {
	rep := &Representation{ID: id, Bandwidth: bandwidth, Data: data}
	var blocks []ebml.Block
	doc, err := ebml.Parse(bytes.NewReader(data), ebml.DefaultParseOptions(), func(b ebml.Block) error {
		blocks = append(blocks, b)
		return nil
	})
	if err != nil {
		return nil, inputErr("parse", err)
	}
	rep.Doc = doc
	types := make(map[uint64]ebml.TrackType, len(doc.Tracks))
	for _, t := range doc.Tracks {
		types[t.Number] = t.Type
	}
	ix := index.New(doc.Tracks)
	clocks := make(map[uint64]*timeline.TrackClock, len(doc.Tracks))
	for _, b := range blocks {
		tc, ok := clocks[b.Track]
		if !ok {
			tc, err = timeline.NewTrackClock(doc.TimecodeScale, frameNanos, 0)
			if err != nil {
				return nil, inputErr("clock", err)
			}
			clocks[b.Track] = tc
		}
		nanos, err := tc.Observe(b.Timecode, b.Source.Offset)
		if err != nil {
			return nil, inputErr("timeline", err)
		}
		typ := types[b.Track]
		ix.Add(typ, b, nanos)
		if typ == ebml.TrackVideo && b.Keyframe {
			rep.Keyframes = append(rep.Keyframes, nanos)
		}
		if nanos+frameNanos > rep.Duration {
			rep.Duration = nanos + frameNanos
		}
	}
	ix.Finalize()
	rep.Index = ix
	sort.Slice(rep.Keyframes, func(i, j int) bool { return rep.Keyframes[i] < rep.Keyframes[j] })
	return rep, nil
}

// Plan is the aligned segmentation result.
type Plan struct {
	Alignment *timeline.Alignment
	Cuts      []int64
	Config    segment.Config
	Manifests map[string]*segment.Manifest
	// ParseOffset is the byte offset the parser reached for the primary
	// rendition, recorded in checkpoints so runs can resume mid-stream.
	ParseOffset int64
}

// BuildPlan aligns renditions and builds one manifest per rendition.
func BuildPlan(reps []*Representation, cfg Config) (*Plan, error) {
	if len(reps) < 2 {
		return nil, inputErr("align", fmt.Errorf("need at least two representations, have %d", len(reps)))
	}
	alignIn := timeline.AlignInput{TargetDuration: cfg.TargetDuration, Tolerance: cfg.Tolerance}
	for _, r := range reps {
		alignIn.Representations = append(alignIn.Representations, timeline.Representation{
			ID: r.ID, Bandwidth: r.Bandwidth, Frames: r.Keyframes, Duration: r.Duration,
		})
	}
	align, err := timeline.Align(alignIn)
	if err != nil {
		// Preserve a resumable checkpoint naming what was already parsed so an
		// operator can fix or drop the misaligned rendition and rerun without
		// reparsing from scratch.
		if cfg.WorkDir != "" {
			_ = saveRejectedCheckpoint(cfg, reps, err)
		}
		return nil, inputErr("align", err)
	}
	segCfg := cfg.SegmentConfig(reps[0].Doc.TimecodeScale)
	plan := &Plan{Alignment: align, Cuts: align.Cuts, Config: segCfg, Manifests: map[string]*segment.Manifest{}, ParseOffset: reps[0].Doc.Size}
	hasher := spanHasher(reps[0].Data)
	for _, r := range reps {
		video, ok := r.Index.VideoTrack()
		if !ok {
			return nil, inputErr("segment", fmt.Errorf("representation %s has no video track", r.ID))
		}
		m, err := segment.Build(segment.BuildInput{
			Representation: r.ID,
			VideoTrack:     video,
			AudioTracks:    audioTracks(r.Doc.Tracks),
			Index:          r.Index,
			Cuts:           align.Cuts,
			Config:         segCfg,
			InputDigest:    digestBytes(r.Data),
			HashSpan:       hasher,
		})
		if err != nil {
			return nil, inputErr("segment", err)
		}
		plan.Manifests[r.ID] = m
	}
	return plan, nil
}

// saveRejectedCheckpoint records parsed offsets and the alignment rejection so
// a later run can resume. It never marks any segment completed.
func saveRejectedCheckpoint(cfg Config, reps []*Representation, cause error) error {
	cp := pipeline.NewCheckpoint(digestBytes(reps[0].Data), cfg.SegmentConfig(reps[0].Doc.TimecodeScale).Digest())
	cp.ParseOffset = reps[0].Doc.Size
	cp.Rejections = append(cp.Rejections, pipeline.Rejection{Index: -1, Reason: cause.Error()})
	return cp.Save(filepath.Join(cfg.WorkDir, "checkpoint-rejected.json"))
}

func spanHasher(src []byte) func(ebml.Span) ([32]byte, error) {
	return func(sp ebml.Span) ([32]byte, error) {
		if sp.Offset < 0 || sp.End() > int64(len(src)) {
			return [32]byte{}, fmt.Errorf("span [%d,%d) outside %d-byte input", sp.Offset, sp.End(), len(src))
		}
		return sha256.Sum256(src[sp.Offset:sp.End()]), nil
	}
}

func audioTracks(tracks []ebml.Track) []uint64 {
	var out []uint64
	for _, t := range tracks {
		if t.Type == ebml.TrackAudio {
			out = append(out, t.Number)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
