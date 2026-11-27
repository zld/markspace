// Package rollback implements atomic release commits.
//
// Everything the commit needs lives in one directory: the new manifest, the
// rollback copy of the previous manifest, the produced artifacts and a
// journal.  All of them are written to temporary files and renamed into
// place.  Recovery only ever reports one of two states — the complete old
// release or the complete new one — and never a mixture.
package rollback

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"example.com/assetpack/internal/fault"
	"example.com/assetpack/internal/manifest"
)

// File names inside a release directory.
const (
	ManifestName  = "manifest.json"
	RollbackName  = "manifest.prev.json"
	JournalName   = "commit.journal.json"
	ArtifactDir   = "artifacts"
	StagingSuffix = ".staging"
	TempSuffix    = ".tmp"
)

// Phase is the journal state.
type Phase string

const (
	PhaseStaged     Phase = "staged"
	PhaseCommited   Phase = "committed"
	PhaseRolledBack Phase = "rolled-back"
)

// Journal records the intent of a commit so a crash can be classified.
type Journal struct {
	Phase      Phase    `json:"phase"`
	Release    string   `json:"release"`
	Staging    string   `json:"staging_dir"`
	NewDigest  string   `json:"new_digest"`
	OldDigest  string   `json:"old_digest"`
	Artifacts  []string `json:"artifacts"`
	InjectedAt string   `json:"injected_at,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// Checkpoint manages one release directory.
type Checkpoint struct {
	Dir string
}

// New creates a checkpoint rooted at dir, ensuring the directory exists.
func New(dir string) (*Checkpoint, error) {
	if dir == "" {
		return nil, fault.New(fault.StageCommit, fault.KindInvalidArg, "checkpoint needs a directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fault.New(fault.StageCommit, fault.KindIO, "create release directory: %v", err)
	}
	return &Checkpoint{Dir: dir}, nil
}

func (c *Checkpoint) path(name string) string { return filepath.Join(c.Dir, name) }

// ArtifactDir is where committed artifacts live.
func (c *Checkpoint) ArtifactDir() string { return filepath.Join(c.Dir, ArtifactDir) }

// ManifestExists reports whether a committed manifest is present.
func (c *Checkpoint) ManifestExists() bool {
	_, err := os.Stat(c.path(ManifestName))
	return err == nil
}

// ManifestDigest reads the committed manifest digest from disk.
func (c *Checkpoint) ManifestDigest() (string, error) {
	raw, err := os.ReadFile(c.path(ManifestName))
	if err != nil {
		return "", fault.New(fault.StageState, fault.KindIO, "read committed manifest: %v", err)
	}
	m, err := manifest.ReadV2(strings.NewReader(string(raw)))
	if err != nil {
		return "", err
	}
	return m.Digest, nil
}

// ReadManifest loads the committed manifest, or nil when none exists.
func (c *Checkpoint) ReadManifest() (*manifest.V2, error) {
	raw, err := os.ReadFile(c.path(ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.New(fault.StageState, fault.KindIO, "read committed manifest: %v", err)
	}
	m, err := manifest.ReadV2(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	return m, nil
}

// ReadJournal loads the journal, or nil when there is none.
func (c *Checkpoint) ReadJournal() (*Journal, error) {
	raw, err := os.ReadFile(c.path(JournalName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.New(fault.StageState, fault.KindIO, "read journal: %v", err)
	}
	var j Journal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fault.New(fault.StageState, fault.KindMissing, "journal is corrupt: %v", err)
	}
	return &j, nil
}
