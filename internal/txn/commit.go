// Package txn publishes staged artifacts and a manifest atomically, with
// recovery from the "artifacts written, commit marker missing" window.
//
// Commit protocol for a task:
//
//  1. Begin: journal stage_begin
//  2. Stage: write artifacts into a staging directory inside the output root
//     and journal each artifact with its content hash
//  3. Seal: journal manifest_draft (the manifest that will be committed)
//  4. checkpoint after_artifact   <- injectable crash, nothing committed yet
//  5. Publish: rename staging -> published (atomic, same filesystem), journal
//     stage_done
//  6. checkpoint after_stage_done
//  7. Commit: write manifest via temp file + rename, then journal commit
//  8. checkpoint after_manifest
//
// Recovery replays the journal. A task with a durable draft completes
// publication and the commit exactly once; staging files that the journal does
// not register are removed; a committed task is verified and never
// republished. Nothing is ever copied, so artifact counts cannot change.
package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"media.rebuild/internal/journal"
	"media.rebuild/internal/manifest"
	"media.rebuild/internal/model"
)

// CrashPoint names an injectable failure.
type CrashPoint string

const (
	CrashNone           CrashPoint = ""
	CrashAfterArtifact  CrashPoint = "after_artifact"
	CrashAfterStageDone CrashPoint = "after_stage_done"
	CrashAfterManifest  CrashPoint = "after_manifest"
)

// Phases reported when a crash point fires.
const (
	PhaseArtifactWritten = "artifact_written"
	PhaseStageDone       = "stage_done"
	PhaseManifestWritten = "manifest_written"
)

// PhaseOf maps a crash point to the phase reported to the operator.
func PhaseOf(c CrashPoint) string {
	switch c {
	case CrashAfterArtifact:
		return PhaseArtifactWritten
	case CrashAfterStageDone:
		return PhaseStageDone
	case CrashAfterManifest:
		return PhaseManifestWritten
	}
	return "none"
}

// ErrCrashInjected is returned when a crash point fires; callers map it to
// exit status 75.
var ErrCrashInjected = errors.New("crash point reached")

// Engine owns one output root.
type Engine struct {
	root  string
	log   *journal.Log
	crash CrashPoint
	// Crashed is set when the injected crash fired.
	Crashed bool
}

// Root returns the output root.
func (e *Engine) Root() string { return e.root }

// JournalPath returns the journal location inside the root.
func JournalPath(root string) string { return filepath.Join(root, "journal", "rebuild.wal") }

// ManifestPath returns the manifest location inside the root.
func ManifestPath(root string) string { return filepath.Join(root, "manifest.json") }

// OpenRoot prepares the output root and opens its journal.
func OpenRoot(root string, crash CrashPoint) (*Engine, error) {
	if err := validateRoot(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, model.New(model.CodeIO, model.Source{Name: root, Offset: -1}, "create output root", err)
	}
	log, err := journal.Open(JournalPath(root))
	if err != nil {
		return nil, err
	}
	return &Engine{root: root, log: log, crash: crash}, nil
}

// Close closes the journal.
func (e *Engine) Close() error { return e.log.Close() }

func validateRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return model.New(model.CodeInvalidArgument, model.Source{Offset: -1}, "output root is empty", nil)
	}
	clean := filepath.Clean(root)
	if clean == "." || clean == "/" {
		return model.Newf(model.CodeInvalidArgument, model.Source{Name: root, Offset: -1}, "refusing output root %q", root)
	}
	return nil
}

func (e *Engine) stagedDir(task string) string    { return filepath.Join(e.root, "staging", task) }
func (e *Engine) publishedDir(task string) string { return filepath.Join(e.root, "artifacts", task) }

// Begin opens a task in the journal.
func (e *Engine) Begin(task, stage string) error {
	_, err := e.log.Append(journal.Record{Type: journal.TypeStageBegin, Task: task, Stage: stage})
	return err
}

// Stage writes one artifact into the task's staging directory and journals its
// hash. Names must be plain file names; nothing may escape the staging tree.
func (e *Engine) Stage(task, name string, data []byte) (manifest.Artifact, error) {
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." || name == "" {
		return manifest.Artifact{}, model.Newf(model.CodeInvalidArgument, model.Source{Name: name, Offset: -1},
			"artifact name %q must be a plain file name", name)
	}
	dir := e.stagedDir(task)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return manifest.Artifact{}, model.New(model.CodeIO, model.Source{Name: dir, Offset: -1}, "create staging directory", err)
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return manifest.Artifact{}, model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "create artifact", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return manifest.Artifact{}, model.New(model.CodeIO, model.Source{Name: path, Offset: 0}, "write artifact", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return manifest.Artifact{}, model.New(model.CodeIO, model.Source{Name: path, Offset: int64(len(data))}, "sync artifact", err)
	}
	if err := f.Close(); err != nil {
		return manifest.Artifact{}, model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "close artifact", err)
	}
	sum := sha256.Sum256(data)
	art := manifest.Artifact{
		Name:   name,
		File:   filepath.ToSlash(filepath.Join("artifacts", task, name)),
		Bytes:  len(data),
		SHA256: hex.EncodeToString(sum[:]),
	}
	payload, err := json.Marshal(art)
	if err != nil {
		return manifest.Artifact{}, model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "encode artifact record", err)
	}
	if _, err := e.log.Append(journal.Record{Type: journal.TypeArtifact, Task: task, Payload: payload}); err != nil {
		return manifest.Artifact{}, err
	}
	return art, nil
}

// Seal journals the manifest that will be committed and then hits the
// after_artifact crash point when it is selected. Sealing is what makes a
// crashed task recoverable rather than merely discardable.
func (e *Engine) Seal(m manifest.Manifest) error {
	m.Version = manifest.Version
	m.Committed = true
	if m.Artifacts == nil {
		m.Artifacts = []manifest.Artifact{}
	}
	sortArtifacts(m.Artifacts)
	payload, err := json.Marshal(m)
	if err != nil {
		return model.New(model.CodeIO, model.Source{Name: e.root, Offset: -1}, "encode manifest draft", err)
	}
	if _, err := e.log.Append(journal.Record{Type: journal.TypeManifestDraft, Task: m.Task, Payload: payload}); err != nil {
		return err
	}
	return e.Checkpoint(CrashAfterArtifact)
}

// Checkpoint triggers the injected crash when point is the selected one.
func (e *Engine) Checkpoint(point CrashPoint) error {
	if point != CrashNone && e.crash == point {
		e.Crashed = true
		return fmt.Errorf("%w: %s", ErrCrashInjected, point)
	}
	return nil
}

// Publish renames the task's staging directory into place and journals
// stage_done. It is idempotent when the published directory already exists and
// staging does not.
func (e *Engine) Publish(task string) error {
	staged := e.stagedDir(task)
	published := e.publishedDir(task)
	if err := os.MkdirAll(filepath.Dir(published), 0o755); err != nil {
		return model.New(model.CodeIO, model.Source{Name: published, Offset: -1}, "create artifacts root", err)
	}
	if _, err := os.Stat(staged); err != nil {
		if os.IsNotExist(err) {
			if dirExists(published) {
				return nil
			}
			return model.New(model.CodeIO, model.Source{Name: staged, Offset: -1}, "staging directory missing", err)
		}
		return model.New(model.CodeIO, model.Source{Name: staged, Offset: -1}, "stat staging", err)
	}
	// Replacing a published directory would let a rerun duplicate artifacts.
	if dirExists(published) {
		return model.Newf(model.CodeTxnConflict, model.Source{Name: published, Offset: -1},
			"task %q already has a published directory", task)
	}
	if err := os.Rename(staged, published); err != nil {
		return model.New(model.CodeIO, model.Source{Name: staged, Offset: -1}, "publish staging directory", err)
	}
	if _, err := e.log.Append(journal.Record{Type: journal.TypeStageDone, Task: task}); err != nil {
		return err
	}
	return e.Checkpoint(CrashAfterStageDone)
}

// Commit writes the manifest atomically and then journals the commit marker.
func (e *Engine) Commit(m manifest.Manifest) error {
	m.Version = manifest.Version
	m.Committed = true
	if m.Artifacts == nil {
		m.Artifacts = []manifest.Artifact{}
	}
	sortArtifacts(m.Artifacts)
	data, err := m.Encode()
	if err != nil {
		return err
	}
	if err := writeFileAtomic(ManifestPath(e.root), data); err != nil {
		return err
	}
	if err := e.Checkpoint(CrashAfterManifest); err != nil {
		return err
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return model.New(model.CodeIO, model.Source{Name: e.root, Offset: -1}, "encode commit record", err)
	}
	if _, err := e.log.Append(journal.Record{Type: journal.TypeCommit, Task: m.Task, Payload: payload}); err != nil {
		return err
	}
	return nil
}

func sortArtifacts(a []manifest.Artifact) {
	sort.SliceStable(a, func(i, j int) bool { return a[i].Name < a[j].Name })
}

// writeFileAtomic writes data to a sibling temp file, fsyncs it, and renames.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return model.New(model.CodeIO, model.Source{Name: dir, Offset: -1}, "create manifest temp", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return model.New(model.CodeIO, model.Source{Name: tmpName, Offset: 0}, "write manifest temp", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return model.New(model.CodeIO, model.Source{Name: tmpName, Offset: int64(len(data))}, "sync manifest temp", err)
	}
	if err := tmp.Close(); err != nil {
		return model.New(model.CodeIO, model.Source{Name: tmpName, Offset: -1}, "close manifest temp", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return model.New(model.CodeIO, model.Source{Name: path, Offset: -1}, "rename manifest", err)
	}
	return nil
}

// State summarizes what the journal says about one task.
type State struct {
	Task           string
	Begin          bool
	Artifacts      []manifest.Artifact
	Draft          *manifest.Manifest
	StageDone      bool
	Committed      bool
	HasStaging     bool
	HasPublished   bool
	StagingFiles   []string
	PublishedFiles []string
}

// Registered reports whether name appears in the journal's artifact records.
func (s *State) Registered(name string) bool {
	for _, a := range s.Artifacts {
		if a.Name == name {
			return true
		}
	}
	return false
}

// Scan replays the journal and inspects the output tree per task.
func (e *Engine) Scan() (map[string]*State, []*model.Error, error) {
	states := make(map[string]*State)
	recs, bad, err := journal.Replay(e.log.Path(), nil)
	if err != nil {
		return nil, nil, err
	}
	for i := range recs {
		r := &recs[i]
		st := states[r.Task]
		if st == nil {
			st = &State{Task: r.Task}
			states[r.Task] = st
		}
		switch r.Type {
		case journal.TypeStageBegin:
			st.Begin = true
		case journal.TypeArtifact:
			var a manifest.Artifact
			if err := json.Unmarshal(r.Payload, &a); err != nil {
				bad = append(bad, model.New(model.CodeJournalCorrupt, model.Source{Name: e.log.Path(), Offset: -1},
					"artifact record payload does not decode", err))
				continue
			}
			st.Artifacts = append(st.Artifacts, a)
		case journal.TypeManifestDraft:
			var m manifest.Manifest
			if err := json.Unmarshal(r.Payload, &m); err != nil {
				bad = append(bad, model.New(model.CodeJournalCorrupt, model.Source{Name: e.log.Path(), Offset: -1},
					"manifest draft does not decode", err))
				continue
			}
			st.Draft = &m
		case journal.TypeStageDone:
			st.StageDone = true
		case journal.TypeCommit:
			st.Committed = true
		}
	}
	for _, st := range states {
		st.HasStaging = dirExists(e.stagedDir(st.Task))
		st.HasPublished = dirExists(e.publishedDir(st.Task))
		if st.HasStaging {
			st.StagingFiles = listFiles(e.stagedDir(st.Task))
		}
		if st.HasPublished {
			st.PublishedFiles = listFiles(e.publishedDir(st.Task))
		}
		sortArtifacts(st.Artifacts)
	}
	return states, bad, nil
}

// RecoveryResult is the outcome of Recover.
type RecoveryResult struct {
	Recovered int            `json:"recovered"`
	Published int            `json:"published"`
	Missing   int            `json:"missing"`
	Duplicate int            `json:"duplicate"`
	Cleaned   []string       `json:"cleaned"`
	Errors    []*model.Error `json:"errors"`
}

// Recover drives every task to one consistent end state and reports what it
// did. It is safe to run repeatedly: a committed task is only verified.
func (e *Engine) Recover() (*RecoveryResult, error) {
	states, bad, err := e.Scan()
	if err != nil {
		return nil, err
	}
	res := &RecoveryResult{Errors: bad, Cleaned: []string{}}
	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		st := states[name]
		if st.Committed {
			res.Published++
			missing, duplicate := verify(st, st.PublishedFiles)
			res.Missing += missing
			res.Duplicate += duplicate
			// Staging next to a committed task is never part of the
			// published set; drop it.
			if st.HasStaging {
				if err := e.removeStaging(name, res); err != nil {
					return res, err
				}
			}
			continue
		}
		if !st.Begin {
			continue
		}
		if st.Draft == nil {
			// Nothing durable was promised; any partial staging must go.
			if st.HasStaging {
				if err := e.removeStaging(name, res); err != nil {
					return res, err
				}
			}
			if st.HasPublished {
				res.Errors = append(res.Errors, model.Newf(model.CodeTxnConflict, model.Source{Name: e.log.Path(), Offset: -1},
					"task %q published artifacts without a manifest draft", name))
				res.Published++
				res.Missing += len(st.PublishedFiles)
			}
			continue
		}
		if !dirsMatch(st.StagingFiles, st.PublishedFiles) {
			if st.HasPublished {
				// The published set is authoritative; a partial staging
				// tree is an unregistered leftover.
				if err := e.removeStaging(name, res); err != nil {
					return res, err
				}
			}
		}
		if !st.HasPublished {
			if !st.HasStaging {
				res.Errors = append(res.Errors, model.Newf(model.CodeTxnConflict, model.Source{Name: e.log.Path(), Offset: -1},
					"task %q was sealed but its artifacts are gone", name))
				continue
			}
			if err := os.MkdirAll(filepath.Dir(e.publishedDir(name)), 0o755); err != nil {
				return res, model.New(model.CodeIO, model.Source{Name: e.publishedDir(name), Offset: -1}, "create artifacts root", err)
			}
			if err := os.Rename(e.stagedDir(name), e.publishedDir(name)); err != nil {
				return res, model.New(model.CodeIO, model.Source{Name: e.stagedDir(name), Offset: -1}, "recover publish", err)
			}
			st.HasStaging = false
			st.HasPublished = true
			st.PublishedFiles = listFiles(e.publishedDir(name))
		} else if st.HasStaging {
			if err := e.removeStaging(name, res); err != nil {
				return res, err
			}
		}
		missing, duplicate := verify(st, st.PublishedFiles)
		res.Missing += missing
		res.Duplicate += duplicate
		data, err := st.Draft.Encode()
		if err != nil {
			return res, err
		}
		if err := writeFileAtomic(ManifestPath(e.root), data); err != nil {
			return res, err
		}
		payload, err := json.Marshal(st.Draft)
		if err != nil {
			return res, err
		}
		if _, err := e.log.Append(journal.Record{Type: journal.TypeCommit, Task: name, Payload: payload}); err != nil {
			return res, err
		}
		if _, err := e.log.Append(journal.Record{Type: journal.TypeRecover, Task: name}); err != nil {
			return res, err
		}
		res.Recovered++
		res.Published++
	}
	model.SortErrors(res.Errors)
	return res, nil
}

// dirsMatch reports whether two file-name sets are identical.
func dirsMatch(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (e *Engine) removeStaging(task string, res *RecoveryResult) error {
	dir := e.stagedDir(task)
	if err := os.RemoveAll(dir); err != nil {
		return model.New(model.CodeIO, model.Source{Name: dir, Offset: -1}, "remove staging", err)
	}
	res.Cleaned = append(res.Cleaned, filepath.ToSlash(filepath.Join("staging", task)))
	return nil
}

// verify compares the registered artifact set with what is published.
func verify(st *State, publishedFiles []string) (missing, duplicate int) {
	seen := make(map[string]struct{}, len(publishedFiles))
	for _, f := range publishedFiles {
		seen[f] = struct{}{}
	}
	registered := make(map[string]struct{}, len(st.Artifacts))
	for _, a := range st.Artifacts {
		registered[a.Name] = struct{}{}
		if _, ok := seen[a.Name]; !ok {
			missing++
		}
	}
	for f := range seen {
		if _, ok := registered[f]; !ok {
			duplicate++
		}
	}
	return missing, duplicate
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func listFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// ReadManifest loads the committed manifest from an output root.
func ReadManifest(root string) (*manifest.Manifest, error) {
	return manifest.Read(ManifestPath(root))
}

// Reason renders the crash point for diagnostics.
func (c CrashPoint) Reason() string {
	if c == CrashNone {
		return "none"
	}
	return fmt.Sprintf("injected crash at %s", c)
}
