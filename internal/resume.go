package kernel

import "path/filepath"

const (
	StageIngest   = "ingest"
	StageParse    = "parse"
	StageCut      = "cut"
	StageManifest = "manifest"

	CheckpointFileName = "checkpoint.json"
	CheckpointVersion  = 1
)

// TrackCheckpoint records one track's committed shared-clock extent.
type TrackCheckpoint struct {
	TrackID     string    `json:"track_id"`
	PID         uint16    `json:"pid"`
	Kind        TrackKind `json:"kind"`
	Start90k    uint64    `json:"start_90k"`
	End90k      uint64    `json:"end_90k"`
	SampleCount int       `json:"sample_count"`
}

// Checkpoint is the resumable state committed after a pipeline stage.
type Checkpoint struct {
	Version       int               `json:"version"`
	Stage         string            `json:"stage"`
	Fixture       string            `json:"fixture"`
	MutateSource  bool              `json:"mutate_source"`
	SourceDigest  string            `json:"source_digest"`
	InputPacket   uint64            `json:"input_packet"`
	Clock         ClockState        `json:"clock"`
	Wraps         int               `json:"wraps"`
	TrackTimes    []TrackCheckpoint `json:"track_times"`
	Gaps          []GapEvent        `json:"gaps"`
	Segments      []*Segment        `json:"segments,omitempty"`
	SilenceMS     int64             `json:"silence_ms"`
	CutCount      int               `json:"cut_count"`
	GapEventCount int               `json:"gap_event_count"`
}

// CheckpointPath returns the safe checkpoint path under out.
func CheckpointPath(out string) (string, error) {
	return SafeJoin(out, CheckpointFileName)
}

// SaveCheckpoint atomically writes the bounded checkpoint.
func SaveCheckpoint(out string, cp *Checkpoint) (string, error) {
	if cp == nil {
		return "", &MediaError{Stage: "resume", Code: "nil_checkpoint", Recoverable: false}
	}
	cp.Version = CheckpointVersion
	path, err := CheckpointPath(out)
	if err != nil {
		return "", err
	}
	if err := WriteJSONAtomic(path, cp, MaxCheckpointBytes); err != nil {
		return path, err
	}
	return path, nil
}

// LoadCheckpoint reads and validates the checkpoint envelope.
func LoadCheckpoint(out string) (*Checkpoint, string, error) {
	path, err := CheckpointPath(out)
	if err != nil {
		return nil, "", err
	}
	var cp Checkpoint
	if err := ReadJSONLimited(path, MaxCheckpointBytes, &cp); err != nil {
		return nil, path, &MediaError{
			Stage:       "resume",
			Code:        "checkpoint_unreadable",
			Message:     "recovery checkpoint could not be read",
			Err:         err,
			Recoverable: false,
		}
	}
	if cp.Version != CheckpointVersion {
		return nil, path, &MediaError{
			Stage:       "resume",
			Code:        "checkpoint_version",
			Message:     "unsupported checkpoint version",
			Recoverable: false,
		}
	}
	if cp.SourceDigest == "" || cp.Stage == "" {
		return nil, path, &MediaError{
			Stage:       "resume",
			Code:        "checkpoint_incomplete",
			Message:     "checkpoint lacks source digest or stage",
			Recoverable: false,
		}
	}
	return &cp, path, nil
}

// TrackCheckpoints extracts checkpointable track time extents.
func TrackCheckpoints(prog *NormalizedProgram) []TrackCheckpoint {
	if prog == nil {
		return nil
	}
	out := make([]TrackCheckpoint, 0, len(prog.Tracks))
	for _, tr := range prog.Tracks {
		out = append(out, TrackCheckpoint{
			TrackID:     tr.ID,
			PID:         tr.PID,
			Kind:        tr.Kind,
			Start90k:    tr.Start90k,
			End90k:      tr.End90k,
			SampleCount: len(tr.Samples),
		})
	}
	return out
}

// CheckpointAbsolutePath is used in terminal recovery-location output.
func CheckpointAbsolutePath(out string) string {
	p, err := filepath.Abs(filepath.Join(out, CheckpointFileName))
	if err != nil {
		return filepath.Join(out, CheckpointFileName)
	}
	return p
}
