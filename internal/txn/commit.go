package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"assetcheck/internal/errmodel"
)

// Artifact is one staged index file ready for atomic publication.
type Artifact struct {
	TxnID       string `json:"txn-id"`
	Key         string `json:"key"` // manifest map key
	Seq         uint64 `json:"seq"`
	StagingPath string `json:"staging-path"`
	FinalPath   string `json:"final-path"`
	SHA256      string `json:"sha256"`
}

// Publisher is implemented by the index store to publish/roll back artifacts.
type Publisher interface {
	Publish(a Artifact) error
	DiscardStaging(a Artifact) error
}

// RecoveryResult explains what recovery did.
type RecoveryResult struct {
	LastCommitSeq  uint64   `json:"last-commit-seq"`
	RolledForward  []string `json:"rolled-forward-txns,omitempty"`
	RolledBack     []string `json:"rolled-back-txns,omitempty"`
	RemovedStaging int      `json:"removed-staging-files"`
}

// Manager groups the WAL with transaction orchestration.
type Manager struct {
	dir string
	wal *WAL
}

// NewManager opens the WAL inside dir.
func NewManager(dir string) (*Manager, error) {
	w, err := OpenWAL(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "staging"), 0o755); err != nil {
		return nil, errmodel.New(errmodel.ErrCommit, "mkdir staging: %v", err)
	}
	return &Manager{dir: dir, wal: w}, nil
}

// Close closes the WAL.
func (m *Manager) Close() error { return m.wal.Close() }

// WAL exposes the append-only log.
func (m *Manager) WAL() *WAL { return m.wal }

// Begin starts a transaction and reserves the next commit sequence.
func (m *Manager) Begin() (txnID string, seq uint64, err error) {
	recs, err := m.wal.ReadAll()
	if err != nil {
		return "", 0, err
	}
	seq = LastCommitSeq(recs) + 1
	txnID = fmt.Sprintf("tx-%06d", m.wal.lsn+1)
	if _, err := m.wal.Append(Record{Type: RecBegin, TxnID: txnID, Seq: seq}); err != nil {
		return "", 0, err
	}
	return txnID, seq, nil
}

// Stage records a named phase and its verifiable checkpoint.
func (m *Manager) Stage(txnID, stage, checkpoint string) error {
	_, err := m.wal.Append(Record{
		Type: RecStage, TxnID: txnID, Stage: stage, Checkpoint: checkpoint,
	})
	return err
}

// RecordPrepared persists the prepare-index WAL entry.
func (m *Manager) RecordPrepared(a Artifact) error {
	_, err := m.wal.Append(Record{
		Type: RecPrepareIndex, TxnID: a.TxnID, Seq: a.Seq, Key: a.Key,
		Staging: a.StagingPath, Final: a.FinalPath, SHA256: a.SHA256,
	})
	return err
}

// Commit writes the commit point, publishes through p, then records applied.
func (m *Manager) Commit(a Artifact, p Publisher) error {
	if _, err := m.wal.Append(Record{
		Type: RecCommit, TxnID: a.TxnID, Seq: a.Seq, Key: a.Key,
		Staging: a.StagingPath, Final: a.FinalPath, SHA256: a.SHA256,
	}); err != nil {
		return err
	}
	if err := p.Publish(a); err != nil {
		return errmodel.New(errmodel.ErrCommit, "publish failed after commit record: %v", err)
	}
	if _, err := m.wal.Append(Record{Type: RecApplied, TxnID: a.TxnID, Seq: a.Seq}); err != nil {
		return err
	}
	return nil
}

// Recover completes committed-but-unapplied transactions and rolls back
// prepared-but-uncommitted ones. It is safe to run on every startup.
func (m *Manager) Recover(p Publisher) (RecoveryResult, error) {
	recs, err := m.wal.ReadAll()
	if err != nil {
		return RecoveryResult{}, err
	}
	res := RecoveryResult{LastCommitSeq: LastCommitSeq(recs)}

	byTx := map[string][]Record{}
	var order []string
	for _, r := range recs {
		if _, ok := byTx[r.TxnID]; !ok {
			order = append(order, r.TxnID)
		}
		byTx[r.TxnID] = append(byTx[r.TxnID], r)
	}
	for _, id := range order {
		rs := byTx[id]
		var commit, prepared *Record
		applied := false
		for i := range rs {
			switch rs[i].Type {
			case RecCommit:
				commit = &rs[i]
			case RecApplied:
				applied = true
			case RecPrepareIndex:
				prepared = &rs[i]
			}
		}
		switch {
		case commit != nil && !applied:
			a := artifactFrom(*commit)
			if err := p.Publish(a); err != nil {
				return res, errmodel.New(errmodel.ErrRecovery, "roll-forward %s failed: %v", id, err)
			}
			_, _ = m.wal.Append(Record{Type: RecApplied, TxnID: id, Seq: commit.Seq, Reason: "recovery-rollforward"})
			res.RolledForward = append(res.RolledForward, id)
		case commit == nil && prepared != nil:
			a := artifactFrom(*prepared)
			if err := p.DiscardStaging(a); err != nil {
				return res, errmodel.New(errmodel.ErrRecovery, "rollback %s failed: %v", id, err)
			}
			_, _ = m.wal.Append(Record{Type: RecRollback, TxnID: id, Seq: prepared.Seq, Reason: "recovery-rollback"})
			res.RolledBack = append(res.RolledBack, id)
		}
	}

	// Whatever remains in staging cannot be visible (never in manifest) and
	// belongs to no completed publication: remove it.
	stagingDir := filepath.Join(m.dir, "staging")
	if entries, err := os.ReadDir(stagingDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if err := os.Remove(filepath.Join(stagingDir, e.Name())); err == nil {
				res.RemovedStaging++
			}
		}
	}
	return res, nil
}

func artifactFrom(r Record) Artifact {
	return Artifact{
		TxnID: r.TxnID, Key: r.Key, Seq: r.Seq,
		StagingPath: r.Staging, FinalPath: r.Final, SHA256: r.SHA256,
	}
}

// Checkpoint computes a stable short SHA-256 checkpoint over parts.
func Checkpoint(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
