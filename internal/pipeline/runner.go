package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediacore/internal/clock"
	"mediacore/internal/index"
)

// Runner executes a resumable batch.
type Runner struct {
	WorkDir      string
	BatchID      string
	ManifestPath string
	Range        index.Range
	SegmentCount int
	CrashAfter   string
	Store        *Store
	State        *State
	Report       Report
	data         *analysis
}

// NewRunner constructs a runner with paths below workDir.
func NewRunner(workDir, manifestPath, batchID string, r index.Range) *Runner {
	if batchID == "" {
		batchID = "demo-batch"
	}
	return &Runner{
		WorkDir: workDir, BatchID: batchID, ManifestPath: manifestPath,
		Range: r, SegmentCount: 3,
		Store: NewStore(filepath.Join(workDir, "state", "pipeline-state.json")),
	}
}

func (rn *Runner) materializeCommitted(stage, path string, payload any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return clock.Wrap(clock.CategoryState, path, 0, 0, "encode "+stage, err)
	}
	hash, recovered, regenerated, err := rn.materialize(stage, path, data)
	if err != nil {
		return err
	}
	if recovered {
		rn.Report.RecoveredArtifacts++
	}
	if regenerated {
		rn.Report.RegeneratedArtifacts++
	}
	rn.State.complete(stage, ArtifactState{Stage: stage, Path: path, SHA256: hash, Verified: stage == "validate"})
	if rn.CrashAfter == stage {
		return ErrSimulatedCrash
	}
	return rn.Store.Save(rn.State)
}

func (rn *Runner) materialize(stage, path string, data []byte) (hash string, recovered, regenerated bool, err error) {
	want := hashBytes(data)
	existing, hashErr := HashFile(path)
	if hashErr == nil {
		if existing == want {
			art, committed := rn.State.Artifacts[stage]
			if !committed || art.SHA256 != want {
				return want, true, false, nil
			}
			return want, false, false, nil
		}
		if _, committed := rn.State.Artifacts[stage]; committed {
			regenerated = true
		}
	} else if !os.IsNotExist(hashErr) {
		return "", false, false, clock.Wrap(clock.CategoryIO, path, 0, 0, "hash existing artifact", hashErr)
	}
	got, werr := WriteAtomicVerified(path, data)
	if werr != nil {
		return "", false, false, werr
	}
	return got, false, regenerated, nil
}
