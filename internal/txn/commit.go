package txn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// StageFunc produces one stage's artifacts from already-normalized inputs.
// It must write only into the provided temp directory and return the list of
// relative artifact paths it created.
type StageFunc func(tempDir string) (artifacts []string, err error)

// ErrStageFailed marks a stage whose work or verification failed.
var ErrStageFailed = errors.New("stage failed")

// Stage is one declarative pipeline step.
type Stage struct {
	Name string
	// InputHash covers the full stage input so recovery can detect changed
	// inputs and force re-execution instead of replaying stale artifacts.
	InputHash string
	Run       StageFunc
}

// Coordinator drives one ordered set of stages against a journal, keeping all
// files under a single bounded project temp directory.
type Coordinator struct {
	journal *Journal
	root    string
	tmpDir  string
	outDir  string
}

// NewCoordinator opens the WAL and prepares tmp/ and out/ subdirectories.
func NewCoordinator(baseDir string) (*Coordinator, error) {
	j, err := OpenJournal(baseDir)
	if err != nil {
		return nil, err
	}
	c := &Coordinator{
		journal: j,
		root:    baseDir,
		tmpDir:  filepath.Join(baseDir, "tmp"),
		outDir:  filepath.Join(baseDir, "out"),
	}
	for _, d := range []string{c.tmpDir, c.outDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Journal exposes the WAL for report generation.
func (c *Coordinator) Journal() *Journal { return c.journal }

// Close releases the WAL handle.
func (c *Coordinator) Close() error { return c.journal.Close() }

func (c *Coordinator) stageTemp(seq int, name string) string {
	return filepath.Join(c.tmpDir, fmt.Sprintf("%02d-%s", seq, name))
}

func (c *Coordinator) stageOut(seq int, name string) string {
	return filepath.Join(c.outDir, fmt.Sprintf("%02d-%s", seq, name))
}

// Execute runs one stage with the full write-ahead protocol:
// append WAL -> write temp -> hash -> verify -> atomic rename -> commit.
func (c *Coordinator) Execute(seq int, st Stage) (string, error) {
	rel := fmt.Sprintf("%02d-%s", seq, st.Name)
	if _, err := c.journal.Log(seq, st.Name, st.InputHash, rel); err != nil {
		return "", err
	}
	tmp := c.stageTemp(seq, st.Name)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	artifacts, err := st.Run(tmp)
	if err != nil {
		_ = c.rollback(seq, st.Name)
		return "", fmt.Errorf("%w: stage %s: %v", ErrStageFailed, st.Name, err)
	}
	if len(artifacts) == 0 {
		_ = c.rollback(seq, st.Name)
		return "", fmt.Errorf("%w: stage %s produced no artifacts", ErrStageFailed, st.Name)
	}
	hash, err := hashDir(tmp)
	if err != nil {
		_ = c.rollback(seq, st.Name)
		return "", err
	}
	if err := c.journal.Prepare(seq, hash); err != nil {
		_ = c.rollback(seq, st.Name)
		return "", err
	}
	target := c.stageOut(seq, st.Name)
	if err := atomicPublish(tmp, target); err != nil {
		_ = c.rollback(seq, st.Name)
		return "", err
	}
	if err := c.journal.Commit(seq); err != nil {
		_ = c.rollback(seq, st.Name)
		return "", err
	}
	return target, nil
}

// rollback removes any temp/published artifacts for a failed stage and writes
// an abort marker. Previously committed stages are untouched, so the package
// returns to its last complete commit boundary.
func (c *Coordinator) rollback(seq int, name string) error {
	_ = os.RemoveAll(c.stageTemp(seq, name))
	_ = os.RemoveAll(c.stageOut(seq, name))
	return c.journal.Abort(seq)
}

// atomicPublish moves a fully written temp directory into place via rename,
// which is atomic on a single local APFS/HFS volume.
func atomicPublish(tmp, target string) error {
	if _, err := os.Stat(target); err == nil {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	return os.Rename(tmp, target)
}

