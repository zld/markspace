package mediacore

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
)

// RollbackRecord is the audit trail of one tentative commit that was undone.
// Retained reports whether an older committed version still covers the slot.
type RollbackRecord struct {
	TrackID  uint32 `json:"track_id"`
	Seq      uint32 `json:"seq"`
	Stage    string `json:"stage"`
	Offset   int64  `json:"offset"`
	Reason   string `json:"reason"`
	Retained bool   `json:"retained"`
	AtNS     Nano   `json:"at_ns"`
}

// journalState is the serializable content of a Journal.
type journalState struct {
	Committed  []Key            `json:"committed"`
	RolledBack []Key            `json:"rolled_back"`
	Rollbacks  []RollbackRecord `json:"rollbacks"`
}

// Journal is the write-ahead ledger of committed fragment slots. Commit is
// idempotent and sorted, so a recovery run replaying the same inputs reaches
// the exact same state and summary. Nothing is "published" until Commit lands.
// The rolledBack set makes a rolled-back slot durable: a recovery replay never
// re-attempts it and never appends a duplicate rollback record.
type Journal struct {
	mu         sync.Mutex
	committed  map[Key]bool
	rolledBack map[Key]bool
	rollbacks  []RollbackRecord
}

// NewJournal starts an empty ledger.
func NewJournal() *Journal {
	return &Journal{committed: map[Key]bool{}, rolledBack: map[Key]bool{}}
}

// LoadJournal reads a ledger previously written with Save. A missing file
// yields an empty journal rather than an error.
func LoadJournal(path string) (*Journal, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewJournal(), nil
	}
	if err != nil {
		return nil, Wrap(err, "", 0, StageRecovery, -1)
	}
	var st journalState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, WrapMsg(err, "", 0, StageRecovery, -1, "corrupt journal")
	}
	j := NewJournal()
	for _, k := range st.Committed {
		j.committed[k] = true
	}
	for _, k := range st.RolledBack {
		j.rolledBack[k] = true
	}
	j.rollbacks = st.Rollbacks
	return j, nil
}

// Save persists the ledger atomically (temp file + rename).
func (j *Journal) Save(path string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := journalState{Rollbacks: j.rollbacks}
	for k := range j.committed {
		st.Committed = append(st.Committed, k)
	}
	for k := range j.rolledBack {
		st.RolledBack = append(st.RolledBack, k)
	}
	sort.Slice(st.Committed, func(i, x int) bool { return keyBefore(st.Committed[i], st.Committed[x]) })
	sort.Slice(st.RolledBack, func(i, x int) bool { return keyBefore(st.RolledBack[i], st.RolledBack[x]) })
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return Wrap(err, "", 0, StageRecovery, -1)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return Wrap(err, "", 0, StageRecovery, -1)
	}
	if err := os.Rename(tmp, path); err != nil {
		return Wrap(err, "", 0, StageRecovery, -1)
	}
	return nil
}

func keyBefore(a, b Key) bool {
	if a.TrackID != b.TrackID {
		return a.TrackID < b.TrackID
	}
	return a.Seq < b.Seq
}

// IsCommitted reports whether a slot already has a durable result.
func (j *Journal) IsCommitted(k Key) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.committed[k]
}

// IsRolledBack reports whether a slot was previously undone and must not be
// attempted again during recovery.
func (j *Journal) IsRolledBack(k Key) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.rolledBack[k]
}

// Commit records slots durably. Re-committing an existing slot is a no-op, and
// a slot previously rolled back is refused, which is what keeps recovery runs
// stable and prevents a bad input from being silently re-published.
func (j *Journal) Commit(keys []Key) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, k := range keys {
		if j.rolledBack[k] {
			continue
		}
		j.committed[k] = true
	}
}

// Rollback undoes tentative slots and appends one audit record per slot that
// is actually removed for the first time. Replaying an already-rolled-back
// slot appends nothing, so repeat runs produce an identical summary.
func (j *Journal) Rollback(tentative []Key, stage string, offset int64, reason string, at Nano) []RollbackRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	var recs []RollbackRecord
	for _, k := range tentative {
		if j.rolledBack[k] {
			continue
		}
		j.rolledBack[k] = true
		if j.committed[k] {
			delete(j.committed, k)
		}
		rec := RollbackRecord{TrackID: k.TrackID, Seq: k.Seq, Stage: stage,
			Offset: offset, Reason: reason, AtNS: at}
		j.rollbacks = append(j.rollbacks, rec)
		recs = append(recs, rec)
	}
	return recs
}

// Committed returns a deterministic, sorted snapshot of committed slots.
func (j *Journal) Committed() []Key {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Key, 0, len(j.committed))
	for k := range j.committed {
		out = append(out, k)
	}
	sort.Slice(out, func(i, x int) bool {
		if out[i].TrackID != out[x].TrackID {
			return out[i].TrackID < out[x].TrackID
		}
		return out[i].Seq < out[x].Seq
	})
	return out
}

// Rollbacks returns all recorded rollback events in occurrence order.
func (j *Journal) Rollbacks() []RollbackRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]RollbackRecord, len(j.rollbacks))
	copy(out, j.rollbacks)
	return out
}

// PublishBatch is the all-or-nothing gate: the candidate slots are staged, the
// gate fn decides whether the batch may be published, and on failure the whole
// tentative set is rolled back so a half-finished batch is never observable.
func (j *Journal) PublishBatch(keys []Key, fn func() error) ([]RollbackRecord, error) {
	var tentative []Key
	for _, k := range keys {
		if j.IsRolledBack(k) || j.IsCommitted(k) {
			continue
		}
		tentative = append(tentative, k)
	}
	j.Commit(tentative)
	if err := fn(); err != nil {
		recs := j.Rollback(tentative, stageOf(err), offsetOf(err), err.Error(), 0)
		return recs, err
	}
	return nil, nil
}

// AbortBatch tentatively commits keys then immediately rolls the batch back
// with an explicit stage/offset/reason. It models a post-merge validation
// failure (for example an RTP anomaly) and returns the recorded rollbacks.
func (j *Journal) AbortBatch(keys []Key, stage string, offset int64, reason string, at Nano) []RollbackRecord {
	var tentative []Key
	for _, k := range keys {
		if j.IsRolledBack(k) || j.IsCommitted(k) {
			continue
		}
		tentative = append(tentative, k)
	}
	j.Commit(tentative)
	return j.Rollback(tentative, stage, offset, reason, at)
}

// RecommitBatch retries a batch after the underlying failure was addressed.
// It clears the durable rolled-back marker for the given keys, then runs the
// normal all-or-nothing gate. Recovery replays of unchanged bad inputs never
// call this, which is what keeps repeat runs stable.
func (j *Journal) RecommitBatch(keys []Key, fn func() error) ([]RollbackRecord, error) {
	j.mu.Lock()
	for _, k := range keys {
		delete(j.rolledBack, k)
	}
	j.mu.Unlock()
	return j.PublishBatch(keys, fn)
}

func stageOf(err error) string {
	var me *Error
	if errors.As(err, &me) {
		return me.Stage
	}
	return StageCommit
}

func offsetOf(err error) int64 {
	var me *Error
	if errors.As(err, &me) {
		return me.Offset
	}
	return -1
}
