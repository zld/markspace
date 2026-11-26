// Package recover persists progress so an interrupted run can resume at the
// last fully verified segment. A checkpoint is only valid for the exact input
// digests it was written against; a changed input digest invalidates the
// affected segment and forces a rescan.
package recover

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"liveregress/internal/fault"
)

// Version is bumped whenever the on-disk shape changes.
const Version = 1

// SegmentState is the committed state of one segment.
type SegmentState struct {
	Layer        string `json:"layer"`
	Sequence     int64  `json:"sequence"`
	Path         string `json:"path"`
	InputDigest  string `json:"input_sha256"`
	OutputDigest string `json:"output_sha256"`
	Bytes        int64  `json:"bytes"`
	Samples      int64  `json:"samples"`
	// Complete is true only when the entire segment was written and verified.
	Complete  bool `json:"complete"`
	Truncated bool `json:"truncated,omitempty"`
	Keyframes int  `json:"keyframes"`
}

// Checkpoint is the resumable run state.
type Checkpoint struct {
	Version int    `json:"version"`
	RunID   string `json:"run_id"`
	Root    string `json:"root"`
	Case    string `json:"case"`
	// Layers holds the last committed segment per layer.
	Layers map[string]LayerState `json:"layers"`
	Done   []SegmentState        `json:"done"`
	Faults int                   `json:"fault_count"`
}

// LayerState records per-layer progress and whether it was isolated.
type LayerState struct {
	Layer    string `json:"layer"`
	LastSeq  int64  `json:"last_sequence"`
	Isolated bool   `json:"isolated"`
	Reason   string `json:"reason,omitempty"`
	Offset   int64  `json:"offset,omitempty"`
}

// New creates an empty checkpoint for a run.
func New(runID, root, testCase string) *Checkpoint {
	return &Checkpoint{Version: Version, RunID: runID, Root: root, Case: testCase,
		Layers: map[string]LayerState{}}
}

// FileName is the checkpoint file name inside a state directory.
const FileName = "checkpoint.json"

// Load reads a checkpoint. A missing file yields (nil, nil).
func Load(dir string) (*Checkpoint, error) {
	path := filepath.Join(dir, FileName)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fault.Wrap(fault.KindPath, path, -1, "open checkpoint", err)
	}
	defer f.Close()
	var cp Checkpoint
	dec := json.NewDecoder(bufio.NewReader(f))
	if err := dec.Decode(&cp); err != nil {
		return nil, fault.Wrap(fault.KindCorrupt, path, -1, "parse checkpoint", err).Recover(true)
	}
	if cp.Version != Version {
		return nil, fault.New(fault.KindCorrupt, path, -1,
			fmt.Sprintf("checkpoint version %d is not supported", cp.Version)).Recover(true)
	}
	if cp.Layers == nil {
		cp.Layers = map[string]LayerState{}
	}
	return &cp, nil
}

// Save writes the checkpoint atomically: a temporary file is fsynced and
// renamed, so a crash never leaves a half-written checkpoint.
func (cp *Checkpoint) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fault.Wrap(fault.KindPath, dir, -1, "create checkpoint dir", err)
	}
	sort.SliceStable(cp.Done, func(i, j int) bool {
		if cp.Done[i].Layer != cp.Done[j].Layer {
			return cp.Done[i].Layer < cp.Done[j].Layer
		}
		return cp.Done[i].Sequence < cp.Done[j].Sequence
	})
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fault.Wrap(fault.KindFormat, dir, -1, "marshal checkpoint", err)
	}
	data = append(data, '\n')
	path := filepath.Join(dir, FileName)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fault.Wrap(fault.KindPath, tmp, -1, "create checkpoint", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fault.Wrap(fault.KindPath, tmp, -1, "write checkpoint", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fault.Wrap(fault.KindPath, tmp, -1, "sync checkpoint", err)
	}
	if err := f.Close(); err != nil {
		return fault.Wrap(fault.KindPath, tmp, -1, "close checkpoint", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fault.Wrap(fault.KindPath, path, -1, "publish checkpoint", err)
	}
	return nil
}

// Commit records a completed segment. Only complete segments are ever
// committed, so resuming always starts at a verified boundary.
func (cp *Checkpoint) Commit(s SegmentState) error {
	if !s.Complete {
		return fault.New(fault.KindFormat, s.Path, -1,
			"refusing to commit an incomplete segment").Recover(false)
	}
	cp.Done = append(cp.Done, s)
	cp.Layers[s.Layer] = LayerState{Layer: s.Layer, LastSeq: s.Sequence, Isolated: false}
	return nil
}

// Isolate marks a layer as stopped at a fault. Other layers continue.
func (cp *Checkpoint) Isolate(s LayerState) {
	cp.Layers[s.Layer] = s
}

// Done returns the committed state for a layer/sequence, if present.
func (cp *Checkpoint) Done0(layer string, seq int64) (SegmentState, bool) {
	for _, s := range cp.Done {
		if s.Layer == layer && s.Sequence == seq {
			return s, true
		}
	}
	return SegmentState{}, false
}

// Reuse decides whether a previously committed segment may be reused without
// rewriting. It requires a matching input digest and a complete record. A
// changed input digest forces a rescan of that segment.
func (cp *Checkpoint) Reuse(layer string, seq int64, inputDigest string) (SegmentState, bool) {
	s, ok := cp.Done0(layer, seq)
	if !ok || !s.Complete || s.InputDigest != inputDigest {
		return SegmentState{}, false
	}
	return s, true
}

// Reset clears progress for a layer whose input changed.
func (cp *Checkpoint) Reset(layer string) {
	kept := cp.Done[:0]
	for _, s := range cp.Done {
		if s.Layer != layer {
			kept = append(kept, s)
		}
	}
	cp.Done = kept
	delete(cp.Layers, layer)
}
