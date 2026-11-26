// Package recovery persists the publish state machine so a process that exits
// after products are written but before the state is committed can be safely
// resumed. A rerun claims existing products by (job key, config hash, segment
// hashes); matching content only has its state recorded, never republished,
// while mismatching content is refused with a conflict error.
package recovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"regress/internal/fault"
)

// State is one stage of the publish lifecycle. Transitions are strictly
// forward: planned -> processing -> staged -> published -> committed.
type State string

const (
	StatePlanned    State = "planned"
	StateProcessing State = "processing"
	StateStaged     State = "staged"
	StatePublished  State = "published"
	StateCommitted  State = "committed"
)

var order = map[State]int{
	StatePlanned: 0, StateProcessing: 1, StateStaged: 2, StatePublished: 3, StateCommitted: 4,
}

// SegmentRecord pins one segment to its content hash and relative file name.
type SegmentRecord struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// Record is the persisted journal entry for one job.
type Record struct {
	JobKey      string          `json:"job_key"`
	ConfigHash  string          `json:"config_hash"`
	State       State           `json:"state"`
	Segments    []SegmentRecord `json:"segments"`
	Manifest    string          `json:"manifest"`
	UpdatedAt   int64           `json:"updated_at_unix_nano"`
	Attempts    int             `json:"attempts"`
}

// Journal is an atomic, mutex-guarded JSON state store.
type Journal struct {
	mu      sync.Mutex
	path    string
	records map[string]*Record
}

// Open loads a journal, creating an empty one when none exists.
func Open(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	j := &Journal{
		path:    filepath.Join(dir, "journal.json"),
		records: map[string]*Record{},
	}
	raw, err := os.ReadFile(j.path)
	if os.IsNotExist(err) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Record
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fault.New(fault.StageRecovery, fault.CodeStateConflict, "",
			"journal parse failed: "+err.Error()).Wrap(err)
	}
	for _, r := range list {
		j.records[r.JobKey] = r
	}
	return j, nil
}

// persist writes the full record list atomically (temp file + rename).
func (j *Journal) persist(nowNano int64) error {
	list := make([]*Record, 0, len(j.records))
	for _, r := range j.records {
		list = append(list, r)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].JobKey < list[b].JobKey })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := j.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, j.path)
}

// Create registers a new job in planned state. Re-creating an existing job key
// with a different config hash is a hard conflict, not an overwrite.
func (j *Journal) Create(jobKey, configHash string, nowNano int64) (*Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if existing, ok := j.records[jobKey]; ok {
		if existing.ConfigHash != configHash {
			return nil, fault.New(fault.StageRecovery, fault.CodeContentConflict, "",
				fmt.Sprintf("job %s exists with config hash %s, refusing %s",
					jobKey, existing.ConfigHash, configHash))
		}
		return existing, nil
	}
	r := &Record{JobKey: jobKey, ConfigHash: configHash, State: StatePlanned, UpdatedAt: nowNano}
	j.records[jobKey] = r
	return r, j.persist(nowNano)
}

// Transition moves a record to a strictly later state.
func (j *Journal) Transition(jobKey string, next State, nowNano int64) (*Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, ok := j.records[jobKey]
	if !ok {
		return nil, fault.New(fault.StageRecovery, fault.CodeStateConflict, "",
			"unknown job key: "+jobKey)
	}
	if order[next] < order[r.State] {
		return nil, fault.New(fault.StageRecovery, fault.CodeStateConflict, "",
			fmt.Sprintf("cannot move %s backwards from %s to %s", jobKey, r.State, next))
	}
	r.State = next
	r.UpdatedAt = nowNano
	if next == StateProcessing {
		r.Attempts++
	}
	return r, j.persist(nowNano)
}

// SetSegments records the staged segment hashes and manifest path.
func (j *Journal) SetSegments(jobKey, manifest string, segs []SegmentRecord, nowNano int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, ok := j.records[jobKey]
	if !ok {
		return fault.New(fault.StageRecovery, fault.CodeStateConflict, "", "unknown job key: "+jobKey)
	}
	r.Segments = append([]SegmentRecord(nil), segs...)
	r.Manifest = manifest
	r.UpdatedAt = nowNano
	return j.persist(nowNano)
}

// Get returns a copy of a record, or nil when absent.
func (j *Journal) Get(jobKey string) *Record {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, ok := j.records[jobKey]
	if !ok {
		return nil
	}
	cp := *r
	cp.Segments = append([]SegmentRecord(nil), r.Segments...)
	return &cp
}
