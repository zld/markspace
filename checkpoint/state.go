// Package checkpoint persists resumable execution state.
package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/zld/mediapipe/index"
	"github.com/zld/mediapipe/media"
)

// FileName is the checkpoint file written inside the work directory.
const FileName = "checkpoint.json"

// Segment records one committed segment output.
type Segment struct {
	Number    int    `json:"number"`
	Layer     string `json:"layer"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	Samples   int    `json:"samples"`
	StartUS   int64  `json:"start_us"`
	EndUS     int64  `json:"end_us"`
	ElapsedUS int64  `json:"elapsed_us"`
}

// State is the durable progress record for one input and parameter set.
//
// A state is only valid for the exact (input digest, index digest, params
// digest) triple that created it. Any change means resume must start over
// rather than trust old outputs.
type State struct {
	Version      string        `json:"version"`
	ID           int64         `json:"id"`
	InputSHA256  string        `json:"input_sha256"`
	IndexDigest  string        `json:"index_digest"`
	ParamsDigest string        `json:"params_digest"`
	SegmentUS    int64         `json:"segment_us"`
	Segments     []Segment     `json:"segments"`
	Issues       []media.Issue `json:"issues,omitempty"`
}

// Options binds the checkpoint to its execution context.
type Options struct {
	Root         string
	InputSHA256  string
	IndexDigest  string
	ParamsDigest string
	Segment      media.Time
}

// Create starts a fresh, uncommitted state.
func Create(opts Options) *State {
	return &State{
		Version: "checkpoint-v1", ID: 1,
		InputSHA256: opts.InputSHA256, IndexDigest: opts.IndexDigest,
		ParamsDigest: opts.ParamsDigest, SegmentUS: opts.Segment.Micros(),
	}
}

// Validate checks that the loaded checkpoint belongs to the current run. A
// failure means the checkpoint must be discarded, not merged.
func (s *State) Validate(opts Options) error {
	if s.Version != "checkpoint-v1" {
		return media.NewError(media.KindCheckpoint, "", "checkpoint version %q is unknown", s.Version)
	}
	if s.InputSHA256 != opts.InputSHA256 {
		return media.NewError(media.KindCheckpoint, "",
			"checkpoint belongs to input %s, current input is %s", s.InputSHA256, opts.InputSHA256)
	}
	if s.IndexDigest != opts.IndexDigest {
		return media.NewError(media.KindCheckpoint, "",
			"checkpoint index digest %s does not match current %s", s.IndexDigest, opts.IndexDigest)
	}
	if s.ParamsDigest != opts.ParamsDigest {
		return media.NewError(media.KindCheckpoint, "",
			"checkpoint parameters differ from current run")
	}
	if s.SegmentUS != opts.Segment.Micros() {
		return media.NewError(media.KindCheckpoint, "",
			"checkpoint segment duration %dus differs from %dus", s.SegmentUS, opts.Segment.Micros())
	}
	return nil
}

// Has reports whether a (layer, number) output is already committed.
func (s *State) Has(layer string, number int) bool {
	for _, seg := range s.Segments {
		if seg.Layer == layer && seg.Number == number {
			return true
		}
	}
	return false
}

// Commit records a finished segment. Callers must only call this after the
// segment file and its digest are on disk. Re-committing the same key is
// idempotent so a retried resume cannot append duplicates.
func (s *State) Commit(seg Segment) error {
	if seg.Layer == "" || seg.Number < 1 {
		return media.NewError(media.KindArgument, "", "invalid segment identity")
	}
	for _, existing := range s.Segments {
		if existing.Layer == seg.Layer && existing.Number == seg.Number {
			if existing.SHA256 != seg.SHA256 {
				return media.NewError(media.KindCheckpoint, "",
					"segment %s/%d already committed with digest %s", seg.Layer, seg.Number, existing.SHA256)
			}
			return nil
		}
	}
	s.Segments = append(s.Segments, seg)
	return nil
}

// SortOrders canonicalizes segment order for stable serialization.
func (s *State) SortOrders() {
	sort.SliceStable(s.Segments, func(i, j int) bool {
		if s.Segments[i].Number != s.Segments[j].Number {
			return s.Segments[i].Number < s.Segments[j].Number
		}
		return s.Segments[i].Layer < s.Segments[j].Layer
	})
}

// Path returns the checkpoint location for root.
func Path(root string) string { return filepath.Join(root, FileName) }

// Load reads a checkpoint, returning (nil, nil) when none exists. A malformed
// or unreadable checkpoint is a classified error: the caller must discard it
// rather than guess.
func Load(root string, opts Options) (*State, error) {
	b, err := os.ReadFile(Path(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, media.WrapError(media.KindCheckpoint, Path(root), err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, media.WrapError(media.KindCheckpoint, Path(root), err)
	}
	if err := s.Validate(opts); err != nil {
		return nil, err
	}
	// Drop committed entries whose files vanished or whose digest changed:
	// a missing or altered output cannot be trusted on resume.
	kept := s.Segments[:0]
	for _, seg := range s.Segments {
		full := filepath.Join(root, seg.Path)
		if _, err := os.Stat(full); err != nil {
			continue
		}
		got, _, err := media.FileSHA256(full)
		if err != nil || got != seg.SHA256 {
			continue
		}
		kept = append(kept, seg)
	}
	s.Segments = kept
	s.SortOrders()
	return &s, nil
}

// Save writes the checkpoint atomically.
func Save(root string, s *State) error {
	s.SortOrders()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return media.WrapError(media.KindInternal, "", err)
	}
	return index.WriteAtomic(Path(root), b)
}

// Discard removes a stale checkpoint.
func Discard(root string) error {
	if err := os.Remove(Path(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return media.WrapError(media.KindIO, Path(root), err)
	}
	return nil
}

// Summary renders a short state description for terminal output.
func (s *State) Summary() string {
	return fmt.Sprintf("checkpoint %d: %d committed segments", s.ID, len(s.Segments))
}
