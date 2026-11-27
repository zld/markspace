// Package state models pipeline stages and the atomic checkpoints that
// make a run resumable. Every completed stage persists input hashes,
// output hashes and a recovery position, written atomically so a crash
// never leaves a half-written checkpoint behind.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"replay/internal/fault"
)

// Phase is a stage lifecycle state.
type Phase string

const (
	// PhasePending is a stage that has not started.
	PhasePending Phase = "pending"
	// PhaseRunning is a stage in progress.
	PhaseRunning Phase = "running"
	// PhaseCompleted is a stage whose outputs are durable.
	PhaseCompleted Phase = "completed"
	// PhaseFailed is a stage that terminated with an error.
	PhaseFailed Phase = "failed"
	// PhaseRolledBack is a stage whose outputs were removed after an
	// upstream input changed.
	PhaseRolledBack Phase = "rolled-back"
)

// allowedTransitions encodes the legal lifecycle graph.
var allowedTransitions = map[Phase][]Phase{
	PhasePending:    {PhaseRunning},
	PhaseRunning:    {PhaseCompleted, PhaseFailed},
	PhaseFailed:     {PhasePending, PhaseRunning},
	PhaseCompleted:  {PhaseRolledBack, PhasePending},
	PhaseRolledBack: {PhasePending, PhaseRunning},
}

// CanTransition reports whether from may move to to.
func CanTransition(from, to Phase) bool {
	for _, candidate := range allowedTransitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// StageCheckpoint is the durable record for one completed stage.
type StageCheckpoint struct {
	// Name is the stage identifier (ingest, timeline, graph, map, merge,
	// manifest).
	Name string `json:"name"`
	// Phase is the last persisted lifecycle state.
	Phase Phase `json:"phase"`
	// InputHash is the SHA-256 of everything that feeds this stage.
	InputHash string `json:"input_hash"`
	// OutputHash is the SHA-256 of this stage's published artefacts.
	OutputHash string `json:"output_hash"`
	// RecoveryPosition is a stage-specific resume point. For ingest it
	// is the number of packets processed, for map the number of mapped
	// assets, and so on.
	RecoveryPosition int `json:"recovery_position"`
	// RecoveryUnit names what RecoveryPosition counts.
	RecoveryUnit string `json:"recovery_unit"`
	// Outputs lists artefact paths this stage wrote, relative to the
	// work directory. They are removed on rollback.
	Outputs []string `json:"outputs,omitempty"`
	// RollbackReason explains why outputs were removed, when they were.
	RollbackReason string `json:"rollback_reason,omitempty"`
}

// CheckpointFile is the on-disk document.
type CheckpointFile struct {
	SchemaVersion int               `json:"schema_version"`
	RunID         string            `json:"run_id"`
	Stages        []StageCheckpoint `json:"stages"`
}

// CheckpointSchemaVersion is bumped when the document shape changes.
const CheckpointSchemaVersion = 1

// Store persists checkpoints under one work directory.
type Store struct {
	dir  string
	path string
}

// NewStore returns a store rooted at dir. The directory is created on
// first write, never on read, so a read-only dry run does not pollute
// the workspace.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, fault.New(fault.KindArgument, "checkpoint store requires a directory").At("state")
	}
	return &Store{dir: dir, path: filepath.Join(dir, "checkpoint.json")}, nil
}

// Path returns the checkpoint file location.
func (s *Store) Path() string { return s.path }

// Load reads the checkpoint file. A missing file yields an empty
// document and no error, which is how a fresh run starts.
func (s *Store) Load() (*CheckpointFile, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return &CheckpointFile{SchemaVersion: CheckpointSchemaVersion}, nil
	}
	if err != nil {
		return nil, fault.New(fault.KindInput, "cannot read checkpoint").
			At("state").Wrap(err)
	}
	var doc CheckpointFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fault.New(fault.KindInput, "checkpoint is not valid JSON").
			At("state").Wrap(err)
	}
	if doc.SchemaVersion != CheckpointSchemaVersion {
		return nil, fault.New(fault.KindInput, "unsupported checkpoint schema version").
			At("state")
	}
	return &doc, nil
}

// Save writes the checkpoint atomically: a temp file in the same
// directory is fsynced and renamed over the target, so readers see either
// the old or the new document, never a partial one.
func (s *Store) Save(doc *CheckpointFile) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fault.New(fault.KindInput, "cannot create work directory").
			At("state").Wrap(err)
	}
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = CheckpointSchemaVersion
	}
	sort.SliceStable(doc.Stages, func(i, j int) bool { return doc.Stages[i].Name < doc.Stages[j].Name })

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fault.New(fault.KindInput, "cannot encode checkpoint").At("state").Wrap(err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(s.dir, ".checkpoint-*.tmp")
	if err != nil {
		return fault.New(fault.KindInput, "cannot create temp checkpoint").At("state").Wrap(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fault.New(fault.KindInput, "cannot write temp checkpoint").At("state").Wrap(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fault.New(fault.KindInput, "cannot sync temp checkpoint").At("state").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return fault.New(fault.KindInput, "cannot close temp checkpoint").At("state").Wrap(err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fault.New(fault.KindInput, "cannot publish checkpoint").At("state").Wrap(err)
	}
	return nil
}

// Stage looks up one stage record.
func (d *CheckpointFile) Stage(name string) (StageCheckpoint, bool) {
	for _, st := range d.Stages {
		if st.Name == name {
			return st, true
		}
	}
	return StageCheckpoint{}, false
}

// Put inserts or replaces a stage record.
func (d *CheckpointFile) Put(cp StageCheckpoint) {
	for i := range d.Stages {
		if d.Stages[i].Name == cp.Name {
			d.Stages[i] = cp
			return
		}
	}
	d.Stages = append(d.Stages, cp)
}

// FirstIncomplete returns the first stage in plan order that is not
// completed, plus its recovery position. When every stage is complete it
// returns ("", 0, false).
func (d *CheckpointFile) FirstIncomplete(plan []string) (string, int, bool) {
	for _, name := range plan {
		cp, ok := d.Stage(name)
		if !ok || cp.Phase != PhaseCompleted {
			position := 0
			if ok {
				position = cp.RecoveryPosition
			}
			return name, position, true
		}
	}
	return "", 0, false
}

// RollbackStages invalidates each named stage and every stage after it in
// the plan, marking phases rolled-back and clearing output hashes so a
// later resume re-runs them. It returns the records it changed.
func (d *CheckpointFile) RollbackStages(plan []string, changed map[string]string) []StageCheckpoint {
	var records []StageCheckpoint
	hit := false
	for _, name := range plan {
		reason, directly := changed[name]
		if directly {
			hit = true
		}
		if !hit {
			continue
		}
		cp, ok := d.Stage(name)
		if !ok || cp.Phase == PhasePending {
			continue
		}
		if reason == "" {
			reason = "downstream of changed input"
		}
		cp.Phase = PhaseRolledBack
		cp.OutputHash = ""
		cp.RecoveryPosition = 0
		cp.RollbackReason = reason
		d.Put(cp)
		records = append(records, cp)
	}
	return records
}

// HashBytes returns the lowercase hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// HashStrings hashes a sequence of strings with length prefixes so that
// concatenation is unambiguous.
func HashStrings(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(h, "%d:", len(part))
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Describe renders a stage for terminal output.
func (cp StageCheckpoint) Describe() string {
	return fmt.Sprintf("%s=%s input=%s output=%s recovered_at=%d %s",
		cp.Name, cp.Phase, shortHash(cp.InputHash), shortHash(cp.OutputHash),
		cp.RecoveryPosition, cp.RecoveryUnit)
}

func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
