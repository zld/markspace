package txn

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Recovery describes the recovery decision taken at startup.
type Recovery struct {
	Interrupted bool   `json:"interrupted"`
	RolledBack  int    `json:"rolled_back"`
	ResumeStage string `json:"resume_stage"`
}

// Txn manages staged, recoverable commits inside Dir.
type Txn struct {
	Dir string
	WAL *WAL
}

func (t *Txn) workTmp() string { return filepath.Join(t.Dir, "work", "tmp") }

// Recover inspects the WAL, removes temp files of incomplete stages and
// reports the stage to resume from. Committed stage outputs are kept so the
// pipeline can resume from the last valid stage.
func (t *Txn) Recover() (*Recovery, error) {
	rec := &Recovery{ResumeStage: StageParse}
	if len(t.WAL.Records) == 0 {
		return rec, nil
	}
	rec.ResumeStage = "none"
	for _, s := range Stages {
		if !t.WAL.Done(s) {
			rec.ResumeStage = s
			break
		}
	}
	if rec.ResumeStage == "none" {
		return rec, nil
	}
	rec.Interrupted = true
	for _, s := range Stages {
		if t.WAL.Done(s) {
			continue
		}
		p := filepath.Join(t.workTmp(), s+".json")
		if s == StageCommit {
			p = filepath.Join(t.workTmp(), "commit.tmp")
		}
		if err := os.Remove(p); err == nil {
			rec.RolledBack++
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return rec, nil
}

// WriteStage persists a stage's intermediate output under work/tmp.
func (t *Txn) WriteStage(stage string, data []byte) error {
	if err := os.MkdirAll(t.workTmp(), 0o755); err != nil {
		return err
	}
	p := filepath.Join(t.workTmp(), stage+".json")
	return writeSync(p, data)
}

// ReadStage loads a previously completed stage's intermediate output.
func (t *Txn) ReadStage(stage string) ([]byte, error) {
	return os.ReadFile(filepath.Join(t.workTmp(), stage+".json"))
}

// CommitAtomic writes data to a same-volume temp file, fsyncs it and
// atomically renames it to Dir/finalName, then fsyncs the directory.
func (t *Txn) CommitAtomic(finalName string, data []byte) error {
	if err := os.MkdirAll(t.workTmp(), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(t.workTmp(), "commit.tmp")
	if err := writeSync(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(t.Dir, finalName)); err != nil {
		return err
	}
	if d, err := os.Open(t.Dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func writeSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
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
	return f.Close()
}
