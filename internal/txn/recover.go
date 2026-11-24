package txn

import (
	"errors"
	"os"

	"medpipe/internal/fault"
)

// RecoveryAction describes what resume did.
type RecoveryAction string

const (
	ActionNone      RecoveryAction = "none"
	ActionCommitted RecoveryAction = "recovered_commit"
	ActionRollback  RecoveryAction = "rolled_back"
)

// RecoveryResult is the observable result of recovery.
type RecoveryResult struct {
	Action        RecoveryAction `json:"action"`
	TxnID         string         `json:"txn_id,omitempty"`
	FinalPath     string         `json:"final_path,omitempty"`
	OutputSHA256  string         `json:"output_sha256,omitempty"`
	RollbackCount int            `json:"rollback_count"`
}

// Recover validates the last WAL transaction and either completes its atomic
// commit or rolls its temporary output back.
func (m *Manager) Recover(verify VerifyFunc) (*RecoveryResult, *fault.Error) {
	recs, fe := m.WAL.ReadRecords()
	if fe != nil {
		return &RecoveryResult{Action: ActionNone, RollbackCount: m.RollbackCount}, fe
	}
	if len(recs) == 0 {
		return &RecoveryResult{Action: ActionNone}, nil
	}
	last := recs[len(recs)-1]
	res := &RecoveryResult{TxnID: last.TxnID, FinalPath: last.FinalPath}

	switch last.State {
	case StateCommitted:
		res.Action = ActionNone
		res.OutputSHA256 = last.OutputSHA256
		return res, nil
	case StateRolledBack:
		res.Action = ActionNone
		return res, nil
	case StatePrepared:
		t := m.transactionFromRecord(last)
		sum, size, herr := HashFile(t.TempPath)
		if herr != nil || sum != last.OutputSHA256 {
			_ = t.Rollback("prepared output digest missing or mismatch")
			res.Action = ActionRollback
			res.RollbackCount = m.RollbackCount
			if herr != nil {
				return res, herr
			}
			ferr := fault.New(fault.ErrDigestMismatch, "txn.recover", 0, "prepared output digest does not match WAL")
			return res, ferr
		}
		t.OutputSHA, t.OutputSize = sum, size
		if verify != nil {
			if verr := verify(t.TempPath); verr != nil {
				_ = t.Rollback("output failed readability verification")
				res.Action = ActionRollback
				res.RollbackCount = m.RollbackCount
				return res, verr
			}
		}
		marker, cerr := t.Commit()
		if cerr != nil {
			return res, cerr
		}
		res.Action = ActionCommitted
		res.OutputSHA256 = marker.OutputSHA256
		res.RollbackCount = m.RollbackCount
		return res, nil
	case StateStarted:
		t := m.transactionFromRecord(last)
		// A crash after rename but before the committed WAL record leaves a
		// complete final file. Reconcile it rather than rolling it back.
		if last.Stage == "commit_rename" && last.OutputSHA256 != "" {
			if sum, _, herr := HashFile(t.FinalPath); herr == nil && sum == last.OutputSHA256 {
				marker := Marker{Version: 1, OutputPath: t.FinalPath, OutputSHA256: sum, InputSHA256: last.InputSHA256, MappingSHA256: last.MappingSHA256}
				_ = WriteMarker(t.MarkerPath, marker)
				_, _ = m.WAL.Append(Record{TxnID: t.ID, Stage: "recovered_commit", State: StateCommitted, FinalPath: t.FinalPath, OutputSHA256: sum})
				res.Action = ActionCommitted
				res.OutputSHA256 = sum
				return res, nil
			}
		}
		_ = t.Rollback("interrupted before durable commit")
		res.Action = ActionRollback
		res.RollbackCount = m.RollbackCount
		return res, nil
	}
	return res, fault.New(fault.ErrWAL, "txn.recover", 0, "unknown WAL state")
}

func (m *Manager) transactionFromRecord(r Record) *Transaction {
	return &Transaction{
		mgr:          m,
		ID:           r.TxnID,
		CurrentStage: r.Stage,
		InputSHA:     r.InputSHA256,
		MappingSHA:   r.MappingSHA256,
		FinalPath:    r.FinalPath,
		TempPath:     r.TempPath,
		MarkerPath:   r.FinalPath + ".commit.json",
		OutputSHA:    r.OutputSHA256,
		OutputSize:   r.OutputSize,
	}
}

// IsInterruptSentinel lets the demo simulate a process kill without running
// os.Exit inside the transaction package.
var IsInterruptSentinel = func(err error) bool {
	return errors.Is(err, errInterrupt)
}

var errInterrupt = errors.New("simulated process interruption")

// InterruptError returns the sentinel used to simulate a crash after prepare.
func InterruptError() error { return errInterrupt }

// IsNotExist reports whether err is an os.IsNotExist fault.
func IsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
