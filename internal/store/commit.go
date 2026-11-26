// Package store performs atomic, journaled commits of manifest and asset
// files. A commit either fully replaces the published set or leaves the
// previous set untouched; a crash before the switch keeps the old manifest as
// the rollback point and reruns are idempotent.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"replaypipe/internal/timebase"
)

// JournalName is written before any mutation and removed after the switch.
const JournalName = ".replaypipe-journal.json"

// File is one published artifact.
type File struct {
	Name string `json:"name"`
	Data []byte `json:"-"`
	Sum  string `json:"sha256"`
}

// Journal records the in-flight commit so a rerun can roll back cleanly.
type Journal struct {
	State       string   `json:"state"`
	Manifest    string   `json:"manifest"`
	Backup      string   `json:"backup"`
	StartedUnix int64    `json:"started_unix"`
	Files       []string `json:"files"`
	Stage       []string `json:"stage"`
}

// CommitOptions control failure-injection used by the interrupted-migration
// scenario. They are inert in normal operation.
type CommitOptions struct {
	// FailBeforeSwitch makes Commit stop after writing staged files and the
	// journal, simulating a crash prior to the atomic rename.
	FailBeforeSwitch bool
}

// Result describes a completed or rolled-back commit.
type Result struct {
	Committed    bool     `json:"committed"`
	RolledBack   bool     `json:"rolled_back"`
	Idempotent   bool     `json:"idempotent"`
	ManifestPath string   `json:"manifest_path"`
	ManifestSHA  string   `json:"manifest_sha256"`
	PreviousSHA  string   `json:"previous_sha256,omitempty"`
	BackupPath   string   `json:"backup_path,omitempty"`
	Files        []string `json:"files"`
}

// Commit atomically publishes manifest plus asset files under root. It:
//  1. writes a journal naming the backup,
//  2. stages the new manifest and assets,
//  3. fsyncs and renames the manifest last.
//
// If the new manifest is byte-identical to the current one the commit is a
// no-op and reports Idempotent. If a journal from an interrupted run exists,
// the old manifest is confirmed as the rollback point first.
func Commit(root, manifestName string, manifest []byte, assets []File, opts CommitOptions) (*Result, *timebase.Error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "create root")
	}
	manifestPath := filepath.Join(root, manifestName)
	backupPath := manifestPath + ".rollback"
	newSum := sha256Hex(manifest)
	res := &Result{ManifestPath: manifestPath, ManifestSHA: newSum}

	if journal, e := readJournal(root); e == nil && journal != nil {
		// A previous run stopped before the switch: keep/restore the backup.
		if _, err := os.Stat(backupPath); err == nil {
			res.RolledBack = true
			res.BackupPath = backupPath
			if _, err := os.Stat(manifestPath); err != nil {
				if err := os.Rename(backupPath, manifestPath); err != nil {
					return nil, timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "restore rollback manifest")
				}
				res.PreviousSHA = sha256File(manifestPath)
			}
		}
		_ = os.Remove(filepath.Join(root, JournalName))
	}

	if old, err := os.ReadFile(manifestPath); err == nil {
		oldSum := sha256Hex(old)
		res.PreviousSHA = oldSum
		if oldSum == newSum {
			res.Committed = true
			res.Idempotent = true
			if err := checkAssets(assets); err != nil {
				return nil, err
			}
			return res, nil
		}
		if err := os.WriteFile(backupPath, old, 0o644); err != nil {
			return nil, timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "write rollback manifest")
		}
		res.BackupPath = backupPath
	}

	journal := &Journal{
		State: "staged", Manifest: manifestName, Backup: backupPath,
		StartedUnix: time.Now().Unix(), Files: assetNames(assets),
		Stage: []string{"journal", "backup"},
	}
	if err := writeJournal(root, journal); err != nil {
		return nil, err
	}

	// Stage assets first; they are content-addressed by filename in callers.
	for _, a := range assets {
		if sha256Hex(a.Data) != a.Sum {
			return nil, timebase.Newf(timebase.CodeDigestMismatch, 0, -1,
				"asset %q digest differs from declared sha256", a.Name)
		}
		if err := writeFileAtomic(filepath.Join(root, a.Name), a.Data); err != nil {
			return nil, timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "stage asset %s", a.Name)
		}
		res.Files = append(res.Files, a.Name)
	}
	if err := writeFileAtomic(manifestPath+".next", manifest); err != nil {
		return nil, timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "stage manifest")
	}
	journal.Stage = append(journal.Stage, "assets", "manifest")
	if err := writeJournal(root, journal); err != nil {
		return nil, err
	}
	if opts.FailBeforeSwitch {
		return nil, timebase.Newf(timebase.CodeInterrupted, 0, -1,
			"interrupted before atomic switch; rollback manifest at %s", backupPath)
	}
	if err := os.Rename(manifestPath+".next", manifestPath); err != nil {
		return nil, timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "switch manifest")
	}
	journal.State = "committed"
	_ = writeJournal(root, journal)
	_ = os.Remove(filepath.Join(root, JournalName))
	res.Committed = true
	return res, nil
}

func checkAssets(assets []File) *timebase.Error {
	for _, a := range assets {
		if sha256Hex(a.Data) != a.Sum {
			return timebase.Newf(timebase.CodeDigestMismatch, 0, -1,
				"asset %q digest differs from declared sha256", a.Name)
		}
	}
	return nil
}

func assetNames(assets []File) []string {
	out := make([]string, 0, len(assets))
	for _, a := range assets {
		out = append(out, a.Name)
	}
	sort.Strings(out)
	return out
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func writeJournal(root string, j *Journal) *timebase.Error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "encode journal")
	}
	if err := writeFileAtomic(filepath.Join(root, JournalName), data); err != nil {
		return timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "write journal")
	}
	return nil
}

func readJournal(root string) (*Journal, error) {
	data, err := os.ReadFile(filepath.Join(root, JournalName))
	if err != nil {
		return nil, err
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

// Recover reports whether an interrupted commit left a rollback point and a
// journal behind, without mutating anything.
func Recover(root string, manifestName string) (rollback bool, journalState string) {
	if j, err := readJournal(root); err == nil && j != nil {
		journalState = j.State
	}
	if _, err := os.Stat(filepath.Join(root, manifestName+".rollback")); err == nil {
		rollback = true
	}
	return rollback, journalState
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sha256File(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return sha256Hex(data)
}

var _ = fmt.Sprintf
