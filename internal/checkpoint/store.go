package checkpoint

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const FileName = "checkpoint.json"

type State string

const (
	StateQueued    State = "queued"
	StateScanning  State = "scanning"
	StateMerging   State = "merging"
	StateRendering State = "rendering"
	StateCommitted State = "committed"
	StatePaused    State = "paused"
)

// Checkpoint is the durable resume point. Coordinates identify the last track
// and zero-based record position that were fully committed.
type Checkpoint struct {
	Schema             string `json:"schema"`
	State              State  `json:"state"`
	LastCommittedTrack string `json:"last_committed_track"`
	LastRecordPosition int    `json:"last_record_position"`
	CommittedUnits     int64  `json:"committed_units"`
	RenderedFrames     int    `json:"rendered_frames"`
	RootHash           string `json:"root_hash,omitempty"`
	CancelReason       string `json:"cancel_reason,omitempty"`
}

func New() *Checkpoint {
	return &Checkpoint{Schema: "mediasync-checkpoint/v1", State: StateQueued}
}

func Path(outDir string) string {
	return filepath.Join(outDir, FileName)
}

func Load(outDir string) (*Checkpoint, error) {
	data, err := os.ReadFile(Path(outDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	if cp.State == "" {
		cp.State = StateQueued
	}
	return &cp, nil
}

// Save atomically replaces checkpoint.json so a canceled process never leaves
// a partially written JSON document.
func (c *Checkpoint) Save(outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	final := Path(outDir)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	return nil
}

func (c *Checkpoint) CommitPosition(trackID string, recordPosition int, units int64) {
	c.LastCommittedTrack = trackID
	c.LastRecordPosition = recordPosition
	c.CommittedUnits = units
}

func (c *Checkpoint) Pause(reason string) {
	c.State = StatePaused
	if reason != "" {
		c.CancelReason = reason
	}
}
