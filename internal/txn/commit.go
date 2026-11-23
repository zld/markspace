package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"

	"mediacheck/internal/model"
	"mediacheck/internal/safeio"
)

// StageFunc validates one pipeline stage. Returning an error aborts the
// transaction; stage order is parse -> probe -> normalize -> validate.
type StageFunc func(payload map[string]interface{}) error

// Manager runs staged transactions over one journal.
type Manager struct {
	journal *Journal
	dir     string
	stages  []model.Stage
}

// NewManager wires the stage pipeline in execution order.
func NewManager(j *Journal, dir string) *Manager {
	return &Manager{
		journal: j, dir: dir,
		stages: []model.Stage{
			model.StageParse, model.StageProbe,
			model.StageNormalize, model.StageValidate,
		},
	}
}

// Run executes one transaction. A previously committed id is a deterministic
// no-op so crash replay never double-commits. On any stage failure an abort
// record is written and the last commit boundary remains intact.
func (m *Manager) Run(txnID string, payload map[string]interface{},
	hooks map[model.Stage]StageFunc) (committed bool, err error) {
	if m.journal.IsCommitted(txnID) {
		return false, nil
	}
	hash := hashPayload(payload)
	// 1. Write-ahead: persist the prepared record before any work.
	if err := m.journal.Append(&Entry{
		TxnID: txnID, Stage: model.StageParse, State: StatePrepared,
		Payload: payload, ContentHash: hash,
	}); err != nil {
		return false, err
	}
	// 2. Staged validation in fixed order.
	for _, stage := range m.stages {
		if fn, ok := hooks[stage]; ok {
			if err := fn(payload); err != nil {
				_ = m.journal.Append(&Entry{
					TxnID: txnID, Stage: stage, State: StateAborted,
					Payload: payload, ContentHash: hash,
				})
				return false, model.NewStageError(stage, err)
			}
		}
	}
	// 3. Atomic commit: write a temp file, fsync, rename, then mark the WAL.
	if err := m.atomicWrite(txnID, payload, hash); err != nil {
		_ = m.journal.Append(&Entry{
			TxnID: txnID, Stage: model.StageCommit, State: StateAborted,
			Payload: payload, ContentHash: hash,
		})
		return false, model.NewStageError(model.StageCommit, err)
	}
	if err := m.journal.Append(&Entry{
		TxnID: txnID, Stage: model.StageCommit, State: StateCommitted,
		Payload: payload, ContentHash: hash,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// Recover replays the journal after a restart. Prepared entries with no
// commit marker are rolled back; committed ids are returned exactly once.
func (m *Manager) Recover() (committed []string, rolledBack []string, err error) {
	for _, p := range m.journal.Pending() {
		rolledBack = append(rolledBack, p.TxnID)
		if e := m.journal.Append(&Entry{
			TxnID: p.TxnID, Stage: p.Stage, State: StateAborted,
			Payload: p.Payload, ContentHash: p.ContentHash,
		}); e != nil {
			return nil, nil, e
		}
	}
	return m.journal.CommittedIDs(), rolledBack, nil
}

func (m *Manager) atomicWrite(txnID string, payload map[string]interface{}, hash string) error {
	name := "txn-" + txnID + ".json"
	final, err := safeio.ResolveContained(m.dir, name)
	if err != nil {
		return err
	}
	tmp := final + ".tmp"
	data, err := json.MarshalIndent(struct {
		TxnID       string                 `json:"txn_id"`
		ContentHash string                 `json:"content_hash"`
		Payload     map[string]interface{} `json:"payload"`
	}{TxnID: txnID, ContentHash: hash, Payload: payload}, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(data)) > safeio.DefaultLimits().MaxReportBytes {
		return model.ErrReportTooLarge
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// hashPayload produces a stable content hash independent of map iteration.
func hashPayload(payload map[string]interface{}) string {
	// json.Marshal sorts map keys, giving deterministic bytes.
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// FilePath returns the committed record path for a txn id.
func (m *Manager) FilePath(txnID string) (string, error) {
	return safeio.ResolveContained(m.dir, "txn-"+txnID+".json")
}
