package job

import (
	"path/filepath"

	"medpipe/internal/apperr"
	"medpipe/internal/store"
)

type publisher struct {
	state   *State
	root    string
	dir     string
	emitted int
}

func newPublisher(root, jobID string, state *State) *publisher {
	return &publisher{state: state, root: root, dir: filepath.Join(Dir(root, jobID), "artifacts")}
}

// Emitted is the number of artifacts atomically published by this process.
func (p *publisher) Emitted() int { return p.emitted }

// Publish implements committed-state verification, prepared-artifact adoption,
// write-ahead state, atomic replacement and optional after-artifact fault.
func (p *publisher) Publish(stageName, fileName string, dataFn func() ([]byte, error), faultPoint string) (string, error) {
	st := p.state.Stage(stageName)
	path := filepath.Join(p.dir, fileName)

	if st.Status == StageCommitted && st.SHA256 != "" {
		if got, _, err := store.Verify(path, st.SHA256, st.Size); err == nil {
			p.state.PutArtifact(fileName, path, got, st.Size)
			return got, nil
		}
		st.Status = StagePending // corrupt/missing published file: rebuild safely
	}

	if st.Status == StagePrepared && st.SHA256 != "" {
		if got, size, err := store.Verify(path, st.SHA256, st.Size); err == nil {
			st.Status = StageCommitted
			st.Size = size
			if err := p.state.Save(p.root, p.state.JobID); err != nil {
				return "", err
			}
			p.state.PutArtifact(fileName, path, got, size)
			return got, nil // adopted uncommitted artifact; not republished
		}
	}

	data, err := dataFn()
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	wantSHA := store.HashBytes(data)

	st.Status = StagePrepared
	st.Artifact = path
	st.SHA256 = wantSHA
	st.Size = int64(len(data))
	st.Attempts++
	if err := p.state.Save(p.root, p.state.JobID); err != nil {
		return "", err
	}

	gotSHA, err := store.WriteAtomic(path, data)
	if err != nil {
		return "", err
	}
	if gotSHA != wantSHA {
		return "", apperr.New("E_ARTIFACT_HASH", "hash mismatch while publishing %s", fileName).WithStage("publish")
	}
	if _, _, err := store.Verify(path, wantSHA, int64(len(data))); err != nil {
		return "", err
	}

	if faultPoint == "after-artifact" && fileName == timelineName {
		return wantSHA, apperr.New("E_FAULT_INJECTED", "fault after %s atomic publish, before commit", fileName).
			WithStage("publish").WithRecoverable(true)
	}

	st.Status = StageCommitted
	if err := p.state.Save(p.root, p.state.JobID); err != nil {
		return "", err
	}
	p.state.PutArtifact(fileName, path, wantSHA, int64(len(data)))
	p.emitted++
	return wantSHA, nil
}
