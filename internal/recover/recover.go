// Package recover is the workspace orchestration core: generating the demo
// source, planning and committing fragments, and reconciling a work directory
// after a simulated/real crash or a hash failure. Only affected artifacts are
// rebuilt; untouched fragments are never rewritten.
package recover

import (
	"bytes"
	"io"

	"mediapack/internal/commit"
	"mediapack/internal/manifest"
)

// Result is the reconciliation counters printed by the recover command.
type Result struct {
	Recovered         int `json:"recovered"`
	DuplicatePublish  int `json:"duplicate_publish"`
	MissingArtifact   int `json:"missing_artifact"`
	Rebuilt           int `json:"rebuilt"`
	AdoptedTemp       int `json:"adopted_temp"`
	NewlyPublished    int `json:"newly_published"`
	AlreadyConsistent int `json:"already_consistent"`
	TotalArtifacts    int `json:"total_artifacts"`
}

// Recover reconciles the work directory against its manifest and source.
func Recover(workDir string) (*Result, *manifest.Manifest, error) {
	work, err := checkWork(workDir)
	if err != nil {
		return nil, nil, err
	}
	m, err := manifest.Load(work)
	if err != nil {
		return nil, nil, err
	}
	j, err := commit.OpenJournal(work)
	if err != nil {
		return nil, m, err
	}
	defer j.Close()

	src, err := openSource(work, m)
	if err != nil {
		return nil, m, err
	}
	defer src.Close()

	res := &Result{TotalArtifacts: len(m.Artifacts)}
	for i := range m.Artifacts {
		art := &m.Artifacts[i]
		if hashOK(work, art) {
			res.AlreadyConsistent++
			continue
		}

		switch art.Status {
		case manifest.StatusPrepared:
			// A complete temp product was recorded before the interrupted commit.
			if _, adopted, err := commit.Commit(work, m, j, art.ID); err == nil {
				if adopted {
					res.AdoptedTemp++
				}
				res.Recovered++
				continue
			}
			// Temp/final unusable: fall through to a local rebuild.
			if err := rebuild(work, m, j, src, art); err != nil {
				return res, m, err
			}
			res.Rebuilt++
			res.Recovered++
		case manifest.StatusCommitted, manifest.StatusIsolated:
			// Recorded product is missing or hash-corrupt: rebuild this one.
			if err := rebuild(work, m, j, src, art); err != nil {
				return res, m, err
			}
			res.Rebuilt++
			res.Recovered++
		default:
			// Pending plan entry: first-time publish, not a recovery action.
			if err := rebuild(work, m, j, src, art); err != nil {
				return res, m, err
			}
			res.NewlyPublished++
		}
	}

	rep, err := evaluate(work, m)
	if err != nil {
		return res, m, err
	}
	res.MissingArtifact = countInvalid(work, m)
	res.DuplicatePublish = 0 // valid committed artifacts are never re-published
	m.Stats = manifest.Stats{
		Recovered: res.Recovered, DuplicatePublish: res.DuplicatePublish,
		MissingArtifact: res.MissingArtifact, Rebuilt: res.Rebuilt,
		AdoptedTemp: res.AdoptedTemp, AlreadyConsistent: res.AlreadyConsistent,
	}
	_ = rep
	if err := m.Save(work); err != nil {
		return res, m, err
	}
	return res, m, nil
}

func rebuild(work string, m *manifest.Manifest, j *commit.Journal, src io.ReadSeeker, art *manifest.Artifact) error {
	data, err := FragmentBytes(src, art)
	if err != nil {
		return err
	}
	art.Attempts++ // independent per-representation retry accounting
	art.TempRelPath = art.RelPath + ".part"
	if err := commit.Prepare(work, m, j, art, bytes.NewReader(data)); err != nil {
		return err
	}
	_, _, err = commit.Commit(work, m, j, art.ID)
	return err
}
