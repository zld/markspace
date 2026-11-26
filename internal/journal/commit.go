package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"continuity/internal/media"
)

// Commit writes missing artifacts, verifies existing bytes, then atomically
// commits state. If injectCrash is true it stops after durable artifacts and
// before the state rename, matching the acceptance fault injection point.
func Commit(dir, caseName, tx string, artifacts []Artifact, frames []FrameRecord, sources []string, injectCrash bool) (*Report, error) {
	rep := &Report{StatePath: filepath.Join(dir, stateFile), Published: len(frames)}

	committed, err := loadStateIfPresent(rep.StatePath)
	if err != nil {
		return nil, err
	}
	if committed != nil {
		if err := verifyCommitted(dir, committed, artifacts); err != nil {
			return nil, err
		}
		rep.AlreadyCommitted = true
		rep.Published = committed.FrameCount
		return rep, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fault(media.CodePublication, dir, err)
	}
	pending, err := recoverPendingChecksums(dir)
	if err != nil {
		return nil, err
	}
	if pending {
		// The acceptance metric counts one recovered interrupted transaction.
		rep.RecoveredArtifacts = 1
	}

	seenNames := map[string]bool{}
	for i, a := range artifacts {
		if seenNames[a.Name] {
			rep.Duplicates++
			return rep, &media.Fault{Phase: media.PhaseJournal, Code: media.CodeDuplicateRef,
				Source: a.Name, Detail: "artifact name appears twice in transaction"}
		}
		seenNames[a.Name] = true
		expected := a.SHA
		if expected == "" {
			sum := sha256.Sum256(a.Data)
			expected = hex.EncodeToString(sum[:])
			a.SHA = expected
			artifacts[i].SHA = expected
		}
		finalPath := filepath.Join(dir, filepath.FromSlash(a.Name))
		exists, err := fileMatches(finalPath, expected)
		if err != nil {
			return rep, err
		}
		if exists {
			rep.ReusedFiles++
			if err := appendJournal(dir, "VERIFIED "+a.Name+" "+expected); err != nil {
				return rep, err
			}
			continue
		}
		if err := atomicWrite(finalPath, a.Data, expected); err != nil {
			return rep, fault(media.CodePublication, finalPath, err)
		}
		if err := appendJournal(dir, "PREPARED "+a.Name+" "+expected); err != nil {
			return rep, err
		}
	}

	if injectCrash {
		if err := appendJournal(dir, "CRASH_POINT artifacts_durable state_uncommitted"); err != nil {
			return rep, err
		}
		return rep, &media.Fault{Phase: media.PhaseJournal, Code: media.CodeInjectedCrash,
			Source: dir, Detail: "artifacts and checksum summary are durable; state commit intentionally skipped"}
	}

	st := &State{Schema: "continuity.journal/v1", Case: caseName, Transaction: tx,
		FrameCount: len(frames), Frames: frames, InputSources: sources}
	for _, a := range artifacts {
		if a.Name == checksumsFile || len(a.Name) > 7 && a.Name[:7] == "frames/" {
			st.Artifacts = append(st.Artifacts, ArtifactRow{Name: a.Name, SHA256: a.SHA})
		}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return rep, err
	}
	stateSum := sha256.Sum256(data)
	stateSHA := hex.EncodeToString(stateSum[:])
	if err := atomicWrite(rep.StatePath, data, stateSHA); err != nil {
		return rep, fault(media.CodePublication, rep.StatePath, err)
	}
	if err := appendJournal(dir, "COMMIT "+tx+" "+stateSHA); err != nil {
		return rep, err
	}
	return rep, nil
}
