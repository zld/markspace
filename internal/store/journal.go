// Package store implements the local, append-oriented stage journal.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
	"archivecheck/internal/plan"
)

const JournalName = "journal.json"

// LayerState is the durable per-representation cursor.
type LayerState struct {
	Bitrate            int64                    `json:"bitrate"`
	Cursor             int                      `json:"cursor"`
	ProcessedFragments int                      `json:"processed_fragments"`
	Verified           bool                     `json:"verified"`
	FragmentSHA256     map[int]string           `json:"fragment_sha256"`
	Recovery           []model.RecoveryPosition `json:"recovery_positions"`
	Boundaries         []model.Boundary         `json:"boundaries"`
}

// Journal is the complete local recovery state for one processing record.
type Journal struct {
	SchemaVersion string                  `json:"schema_version"`
	RecordID      string                  `json:"record_id"`
	InputSHA256   string                  `json:"input_sha256"`
	StageVersion  string                  `json:"stage_version"`
	Parameters    model.Parameters        `json:"parameters"`
	Stage         plan.Stage              `json:"stage"`
	Transitions   []model.StageTransition `json:"stage_transitions"`
	Layers        map[int64]*LayerState   `json:"layers"`
	Artifacts     []model.ArtifactSummary `json:"artifacts"`
	LastError     *JournalError           `json:"last_error,omitempty"`
}

// JournalError is the durable classified failure.
type JournalError struct {
	Kind          string `json:"kind"`
	Stage         string `json:"stage"`
	AssetID       string `json:"asset_id,omitempty"`
	IntervalIndex *int   `json:"interval_index,omitempty"`
	Message       string `json:"message"`
}

// NewJournal creates a pending-scan journal.
func NewJournal(recordID, inputSHA string, params model.Parameters) *Journal {
	return &Journal{
		SchemaVersion: model.ManifestSchema,
		RecordID:      recordID,
		InputSHA256:   inputSHA,
		StageVersion:  model.StageVersion,
		Parameters:    params,
		Stage:         plan.StagePendingScan,
		Layers:        map[int64]*LayerState{},
	}
}

// Load reads a journal. A missing journal returns nil, false, nil.
func Load(path string) (*Journal, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, apperrors.Wrap(apperrors.KindJournal, err).WithPath(path)
	}
	var j Journal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, false, apperrors.Wrap(apperrors.KindJournal, err).WithPath(path)
	}
	if j.Layers == nil {
		j.Layers = map[int64]*LayerState{}
	}
	return &j, true, nil
}

// SaveAtomic writes canonical JSON through a same-directory temp file and
// rename. It never writes media payload data.
func (j *Journal) SaveAtomic(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return apperrors.Wrap(apperrors.KindJournal, err).WithPath(filepath.Dir(path))
	}
	b, err := model.CanonicalJSON(j)
	if err != nil {
		return apperrors.Wrap(apperrors.KindJournal, err)
	}
	return WriteFileAtomic(path, b, 0o644)
}

// WriteFileAtomic writes b to path via temp file, fsync, rename, and directory
// fsync where supported.
func WriteFileAtomic(path string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return apperrors.Wrap(apperrors.KindIO, err).WithPath(dir)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return apperrors.Wrap(apperrors.KindIO, err).WithPath(tmpName)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return apperrors.Wrap(apperrors.KindIO, err).WithPath(tmpName)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return apperrors.Wrap(apperrors.KindIO, err).WithPath(tmpName)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return apperrors.Wrap(apperrors.KindIO, err).WithPath(tmpName)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return apperrors.Wrap(apperrors.KindIO, err).WithPath(path)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Transition validates and appends a stage migration.
func (j *Journal) Transition(to plan.Stage, reason string) error {
	if err := plan.Transition(j.Stage, to); err != nil {
		return err
	}
	j.Transitions = append(j.Transitions, model.StageTransition{
		Sequence: len(j.Transitions) + 1,
		From:     string(j.Stage),
		To:       string(to),
		Reason:   reason,
	})
	j.Stage = to
	return nil
}

// Fail records a classified failure from the current stage.
func (j *Journal) Fail(err error) error {
	kind := apperrors.KindOf(err)
	if kind == "" {
		kind = apperrors.KindIO
	}
	je := &JournalError{Kind: string(kind), Message: err.Error()}
	var ce *apperrors.Error
	if asAppError(err, &ce) {
		je.Stage = ce.Stage
		je.AssetID = ce.AssetID
		je.IntervalIndex = ce.IntervalIndex
	}
	j.LastError = je
	return j.Transition(plan.StageFailed, string(kind))
}

func asAppError(err error, target **apperrors.Error) bool {
	for err != nil {
		if e, ok := err.(*apperrors.Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Layer returns the independent state for bitrate, creating only that layer.
func (j *Journal) Layer(bitrate int64) *LayerState {
	l, ok := j.Layers[bitrate]
	if !ok {
		l = &LayerState{Bitrate: bitrate, FragmentSHA256: map[int]string{}}
		j.Layers[bitrate] = l
	}
	if l.FragmentSHA256 == nil {
		l.FragmentSHA256 = map[int]string{}
	}
	return l
}

// AddArtifact replaces an artifact record with the same path, otherwise adds.
func (j *Journal) AddArtifact(a model.ArtifactSummary) {
	for i := range j.Artifacts {
		if j.Artifacts[i].Path == a.Path {
			j.Artifacts[i] = a
			return
		}
	}
	j.Artifacts = append(j.Artifacts, a)
}

// RecordFragment commits one fragment cursor and hash for exactly one layer.
func (l *LayerState) RecordFragment(index int, hash string) error {
	if index < l.Cursor {
		// Re-delivery of an already committed fragment is idempotent if the
		// hash agrees; it must not move another layer's cursor.
		if got := l.FragmentSHA256[index]; got != "" && got != hash {
			return apperrors.E(apperrors.KindLayer,
				fmt.Sprintf("fragment %d hash changed after commit", index)).
				WithBitrate(l.Bitrate)
		}
		return nil
	}
	if index != l.Cursor {
		return apperrors.E(apperrors.KindLayer,
			fmt.Sprintf("fragment cursor gap: cursor=%d index=%d", l.Cursor, index)).
			WithBitrate(l.Bitrate)
	}
	l.FragmentSHA256[index] = hash
	l.Cursor = index + 1
	l.ProcessedFragments = l.Cursor
	return nil
}
