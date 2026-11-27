// Package rollback: staging and promoting a release.
package rollback

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"example.com/assetpack/internal/fault"
	"example.com/assetpack/internal/manifest"
)

// Stage writes a complete candidate release into a staging directory next to
// the live one.  Nothing visible to readers changes during staging.
func (c *Checkpoint) Stage(m *manifest.V2, artifacts map[string][]byte) (string, error) {
	if m == nil {
		return "", fault.New(fault.StageCommit, fault.KindMissing, "no manifest to stage")
	}
	if err := m.Validate(); err != nil {
		return "", err
	}
	if err := verifyArtifactMap(m, artifacts); err != nil {
		return "", err
	}
	stage := c.path(m.Digest + StagingSuffix)
	if err := removeAndMkdir(stage); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(stage, ArtifactDir), 0o755); err != nil {
		return "", fault.New(fault.StageCommit, fault.KindIO, "create staging artifact dir: %v", err)
	}
	names := make([]string, 0, len(artifacts))
	for name := range artifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeFileAtomic(filepath.Join(stage, ArtifactDir, name), artifacts[name], 0o644); err != nil {
			return "", err
		}
	}
	raw, err := m.Encode()
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(stage, ManifestName), raw, 0o644); err != nil {
		return "", err
	}
	old := ""
	if cur, err := c.ReadManifest(); err == nil && cur != nil {
		old = cur.Digest
		// Capture the rollback point now, before anything is promoted.  A
		// failure between staging and the commit rename can then always be
		// undone to this exact manifest.
		if err := copyFileAtomic(c.path(ManifestName), c.path(RollbackName)); err != nil {
			return "", err
		}
	}
	j := &Journal{
		Phase: PhaseStaged, Release: m.Release, Staging: filepath.Base(stage),
		NewDigest: m.Digest, OldDigest: old, Artifacts: names,
	}
	if err := c.writeJournal(j); err != nil {
		return "", err
	}
	return stage, nil
}

// Commit atomically promotes the staged release.  The old manifest is copied
// to the rollback file first, so a crash after the rename still leaves a
// complete previous version behind.
func (c *Checkpoint) Commit(stage string) error {
	j, err := c.ReadJournal()
	if err != nil {
		return err
	}
	if j == nil || j.Phase != PhaseStaged {
		return fault.New(fault.StageCommit, fault.KindMissing, "no staged release to commit")
	}
	if filepath.Base(stage) != j.Staging {
		return fault.New(fault.StageCommit, fault.KindInvalidArg,
			"staging dir %q is not the journalled %q", filepath.Base(stage), j.Staging)
	}
	stagedManifest := filepath.Join(stage, ManifestName)
	raw, err := os.ReadFile(stagedManifest)
	if err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "read staged manifest: %v", err)
	}
	next, err := manifest.ReadV2(strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	if next.Digest != j.NewDigest {
		return fault.New(fault.StageVerify, fault.KindDigest,
			"staged manifest digest %s does not match journal %s", next.Digest, j.NewDigest)
	}
	if err := os.MkdirAll(c.ArtifactDir(), 0o755); err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "create artifact dir: %v", err)
	}
	// Preserve the currently committed manifest for rollback unless staging
	// already captured it.
	if c.ManifestExists() {
		if _, statErr := os.Stat(c.path(RollbackName)); statErr != nil {
			if err := copyFileAtomic(c.path(ManifestName), c.path(RollbackName)); err != nil {
				return err
			}
		}
	}
	// Move every staged artifact into place, each atomically.  Names are
	// content-addressed, so an artifact that already exists is the same
	// bytes; promoting it again is harmless but unnecessary.
	for _, name := range j.Artifacts {
		src := filepath.Join(stage, ArtifactDir, name)
		dst := filepath.Join(c.ArtifactDir(), name)
		if _, statErr := os.Stat(dst); statErr == nil {
			_ = os.Remove(src)
			continue
		}
		if err := renameAtomic(src, dst); err != nil {
			return err
		}
	}
	// The manifest rename is the commit point.
	if err := renameAtomic(stagedManifest, c.path(ManifestName)); err != nil {
		return err
	}
	j.Phase = PhaseCommited
	if err := c.writeJournal(j); err != nil {
		return err
	}
	_ = os.RemoveAll(stage)
	return nil
}

// Rollback restores the previous manifest and removes artifacts that only
// the failed release introduced.
func (c *Checkpoint) Rollback() error {
	j, err := c.ReadJournal()
	if err != nil {
		return err
	}
	if j == nil {
		return fault.New(fault.StageRollback, fault.KindMissing, "no journal: nothing to roll back")
	}
	if j.Phase == PhaseRolledBack {
		// A retried recovery is idempotent.
		return nil
	}
	// A committed journal can still be explicitly rolled back as long as the
	// rollback copy is present; that is the documented operator escape hatch
	// and is what makes a release reversible after the fact.
	prev := c.path(RollbackName)
	if _, statErr := os.Stat(prev); statErr == nil {
		if err := renameAtomic(prev, c.path(ManifestName)); err != nil {
			return err
		}
	} else if j.OldDigest == "" {
		// There was no previous release: remove the manifest introduced by
		// the failed commit so the directory returns to an empty state.
		_ = os.Remove(c.path(ManifestName))
	} else {
		return fault.New(fault.StageRollback, fault.KindMissing,
			"rollback copy missing for previous digest %s", j.OldDigest)
	}
	// Remove staged and journal state, then prune artifacts that no longer
	// belong to the restored manifest.  Content-addressed names make this
	// safe: anything the restored manifest still references is untouched.
	if j.Staging != "" {
		_ = os.RemoveAll(c.path(j.Staging))
	}
	_ = c.pruneArtifacts()
	j.Phase = PhaseRolledBack
	if err := c.writeJournal(j); err != nil {
		return err
	}
	return nil
}

// RecoveryState describes the result of inspecting a directory after a crash.
type RecoveryState struct {
	Release  string `json:"release"`
	Digest   string `json:"digest"`
	OldOrNew string `json:"state"` // "old", "new" or "empty"
	Mixed    bool   `json:"mixed"`
	Detail   string `json:"detail"`
}

func (c *Checkpoint) writeJournal(j *Journal) error {
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "encode journal: %v", err)
	}
	return writeFileAtomic(c.path(JournalName), append(raw, '\n'), 0o644)
}

// verifyArtifactMap checks that the staged bytes match the manifest.
func verifyArtifactMap(m *manifest.V2, artifacts map[string][]byte) error {
	digests := make(map[string]string, len(artifacts))
	for name, data := range artifacts {
		sum := sha256.Sum256(data)
		digests[name] = hex.EncodeToString(sum[:])
	}
	return m.Verify(digests)
}
