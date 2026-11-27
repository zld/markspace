// Package publish implements a recoverable publishing ledger.
//
// Publishing is two-phase: the artifact bytes are flushed to disk first and
// only then is its ledger entry atomically committed (write temp file +
// rename). A crash between those two steps leaves an orphaned artifact on
// disk with no committed state. On rerun the ledger detects that orphan,
// verifies its digest, and either records it once (no re-publish) or
// quarantines it when the digest does not match, forcing regeneration.
package publish

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"media-check/internal/assets"
	"media-check/internal/fault"
)

// State is the lifecycle status of one published artifact.
type State string

const (
	// StateWritten means bytes are on disk but the entry is not committed.
	StateWritten State = "written"
	// StateCommitted means the artifact is durably recorded in the ledger.
	StateCommitted State = "committed"
	// StateQuarantined means digest verification failed on rerun.
	StateQuarantined State = "quarantined"
)

// Artifact is one versioned, hash-verified published output.
type Artifact struct {
	Version      string    `json:"version"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Digest       string    `json:"digest"`
	SourceDigest string    `json:"source_digest"`
	State        State     `json:"state"`
	CreatedAt    time.Time `json:"created_at"`
	CommittedAt  time.Time `json:"committed_at,omitempty"`
}

// RecoveryReport summarizes what a rerun did with on-disk artifacts.
type RecoveryReport struct {
	// Reused counts orphan artifacts verified against their expected digest
	// and adopted into the ledger without rewriting their bytes.
	Reused int `json:"reused"`
	// Republished counts artifacts regenerated and published anew.
	Republished int `json:"republished"`
	// MissedLedger counts artifacts still unrecorded after recovery; a
	// correct rerun leaves this at zero because every orphan is either
	// adopted or explicitly quarantined.
	MissedLedger int `json:"missed_ledger"`
	// Quarantined counts artifacts whose digest failed verification.
	Quarantined int `json:"quarantined"`
	// OrphansDetected is the raw number of on-disk artifacts found without a
	// committed ledger entry. It is diagnostic: all of them are then either
	// reused or quarantined, so it never contributes to MissedLedger.
	OrphansDetected int `json:"orphans_detected"`
}

// Ledger is the mutex-protected, file-backed publishing state.
type Ledger struct {
	mu        sync.Mutex
	path      string
	dir       string
	artifacts map[string]*Artifact // keyed by version+name
	order     []string
}

// OpenLedger loads (or initializes) a ledger in dir.
func OpenLedger(dir string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o755); err != nil {
		return nil, fmt.Errorf("publish: create dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "quarantine"), 0o755); err != nil {
		return nil, fmt.Errorf("publish: create quarantine dir: %w", err)
	}
	l := &Ledger{
		path:      filepath.Join(dir, "ledger.json"),
		dir:       dir,
		artifacts: make(map[string]*Artifact),
	}
	if err := l.load(); err != nil {
		return nil, err
	}
	return l, nil
}

func key(version, name string) string { return version + "/" + name }

// Stage writes artifact bytes (phase 1) without committing ledger state.
// It returns the artifact descriptor; call Commit to finalize.
func (l *Ledger) Stage(version, name string, src *assets.Source, data []byte) (*Artifact, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	digest := assets.Digest(data)
	rel := filepath.Join("artifacts", version+"_"+name+"_"+digest[:12])
	target := filepath.Join(l.dir, rel)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, fmt.Errorf("publish: stage write: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return nil, fmt.Errorf("publish: stage rename: %w", err)
	}
	a := &Artifact{
		Version: version, Name: name, Path: rel, Digest: digest,
		SourceDigest: src.ID, State: StateWritten, CreatedAt: time.Now().UTC(),
	}
	l.artifacts[key(version, name)] = a
	return a, nil
}

// Commit durably records a staged artifact (phase 2) via atomic file replace.
func (l *Ledger) Commit(version, name string) (*Artifact, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.artifacts[key(version, name)]
	if !ok {
		return nil, fmt.Errorf("publish: nothing staged for %s/%s", version, name)
	}
	a.State = StateCommitted
	a.CommittedAt = time.Now().UTC()
	if err := l.persistLocked(); err != nil {
		return nil, err
	}
	return a, nil
}

// Publish is the convenience one-call path used when no crash is simulated.
func (l *Ledger) Publish(version, name string, src *assets.Source, data []byte) (*Artifact, error) {
	if _, err := l.Stage(version, name, src, data); err != nil {
		return nil, err
	}
	return l.Commit(version, name)
}

// CrashAfterArtifact simulates an exit after bytes hit disk but before the
// ledger entry is committed: it returns the staged descriptor and leaves the
// in-memory entry uncommitted and absent from ledger.json.
func (l *Ledger) CrashAfterArtifact(version, name string, src *assets.Source, data []byte) (*Artifact, error) {
	return l.Stage(version, name, src, data)
}

// Recover reconciles on-disk artifacts with committed ledger state. It is
// safe to call on every rerun. Orphans are verified against expectedDigests
// (version/name -> digest the current build would produce).
func (l *Ledger) Recover(expectedDigests map[string]string) (RecoveryReport, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rep := RecoveryReport{}

	// Orphans: staged in memory but never committed (crash in this process),
	// plus files on disk with no committed ledger entry.
	for k, a := range l.artifacts {
		if a.State == StateCommitted {
			continue
		}
		rep.OrphansDetected++
		if l.reconcileLocked(k, a, expectedDigests, &rep) {
			if err := l.persistLocked(); err != nil {
				return rep, err
			}
		}
	}
	if err := l.reconcileDiskLocked(expectedDigests, &rep); err != nil {
		return rep, err
	}
	// Final invariant: after reconciliation every artifact file must have a
	// committed ledger entry. Anything still unrecorded is a genuine miss.
	rep.MissedLedger = l.countUnrecordedLocked()
	return rep, nil
}

// countUnrecordedLocked returns the number of artifact files with no ledger
// entry after reconciliation has run.
func (l *Ledger) countUnrecordedLocked() int {
	files, err := os.ReadDir(filepath.Join(l.dir, "artifacts"))
	if err != nil {
		return 0
	}
	known := map[string]bool{}
	for _, a := range l.artifacts {
		known[filepath.Base(a.Path)] = true
	}
	missed := 0
	for _, f := range files {
		if !f.IsDir() && !known[f.Name()] {
			missed++
		}
	}
	return missed
}

// reconcileLocked verifies one uncommitted artifact and either commits it
// (digest matches) or quarantines it. It returns true when ledger persisted.
func (l *Ledger) reconcileLocked(k string, a *Artifact, expected map[string]string, rep *RecoveryReport) bool {
	data, err := os.ReadFile(filepath.Join(l.dir, a.Path))
	if err != nil {
		l.quarantineLocked(a, rep)
		return true
	}
	got := assets.Digest(data)
	want, expectKnown := expected[k]
	if got != a.Digest || (expectKnown && want != got) {
		l.quarantineLocked(a, rep)
		return true
	}
	a.State = StateCommitted
	a.CommittedAt = time.Now().UTC()
	rep.Reused++
	return true
}

func (l *Ledger) quarantineLocked(a *Artifact, rep *RecoveryReport) {
	from := filepath.Join(l.dir, a.Path)
	to := filepath.Join(l.dir, "quarantine", filepath.Base(a.Path))
	_ = os.MkdirAll(filepath.Dir(to), 0o755)
	if err := os.Rename(from, to); err == nil {
		a.Path = filepath.Join("quarantine", filepath.Base(a.Path))
	}
	a.State = StateQuarantined
	rep.Quarantined++
}

// reconcileDiskLocked finds artifact files with no ledger entry at all.
func (l *Ledger) reconcileDiskLocked(expected map[string]string, rep *RecoveryReport) error {
	dir := filepath.Join(l.dir, "artifacts")
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, a := range l.artifacts {
		known[filepath.Base(a.Path)] = true
	}
	changed := false
	for _, f := range files {
		if f.IsDir() || known[f.Name()] {
			continue
		}
		rep.OrphansDetected++
		full := filepath.Join(dir, f.Name())
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		got := assets.Digest(data)
		// An unknown orphan can only be re-adopted when the current build
		// expects exactly this digest for some version/name.
		adopted := false
		for k, want := range expected {
			if want != got {
				continue
			}
			ver, name := splitKey(k)
			l.artifacts[k] = &Artifact{
				Version: ver, Name: name,
				Path:   filepath.Join("artifacts", f.Name()),
				Digest: got, State: StateCommitted,
				CommittedAt: time.Now().UTC(),
			}
			rep.Reused++
			adopted = true
			changed = true
			break
		}
		if !adopted {
			_ = os.Rename(full, filepath.Join(l.dir, "quarantine", f.Name()))
			rep.Quarantined++
		}
	}
	if changed {
		return l.persistLocked()
	}
	return nil
}

// VerifyInvariants checks that every committed artifact still exists and
// matches its recorded digest. A failure here is a ledger/restore invariant
// violation (the CLI maps it to exit code 3), not ordinary input damage.
func (l *Ledger) VerifyInvariants() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range l.order {
		a := l.artifacts[k]
		if a == nil || a.State != StateCommitted {
			continue
		}
		full := filepath.Join(l.dir, a.Path)
		data, err := os.ReadFile(full)
		if err != nil {
			return fmt.Errorf("%w: committed artifact %s unreadable: %v",
				fault.ErrRecover, k, err)
		}
		if assets.Digest(data) != a.Digest {
			return fmt.Errorf("%w: committed artifact %s digest drift",
				fault.ErrRecover, k)
		}
	}
	return nil
}

// Committed returns a copy of all committed artifacts in stable order.
func (l *Ledger) Committed() []Artifact {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Artifact
	for _, k := range l.order {
		if a := l.artifacts[k]; a != nil && a.State == StateCommitted {
			out = append(out, *a)
		}
	}
	return out
}

// Find returns a committed artifact by version and name.
func (l *Ledger) Find(version, name string) (*Artifact, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.artifacts[key(version, name)]
	if !ok || a.State != StateCommitted {
		return nil, false
	}
	cp := *a
	return &cp, true
}

func (l *Ledger) load() error {
	b, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("publish: read ledger: %w", err)
	}
	var saved []Artifact
	if err := json.Unmarshal(b, &saved); err != nil {
		return fmt.Errorf("publish: corrupt ledger: %w", err)
	}
	for i := range saved {
		a := saved[i]
		l.artifacts[key(a.Version, a.Name)] = &a
		l.order = append(l.order, key(a.Version, a.Name))
	}
	return nil
}

func (l *Ledger) persistLocked() error {
	l.order = l.order[:0]
	var saved []Artifact
	// Deterministic order: version then name.
	keys := make([]string, 0, len(l.artifacts))
	for k := range l.artifacts {
		keys = append(keys, k)
	}
	// simple stable sort without importing sort twice
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	for _, k := range keys {
		a := l.artifacts[k]
		l.order = append(l.order, k)
		saved = append(saved, *a)
	}
	b, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("publish: write ledger: %w", err)
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return fmt.Errorf("publish: commit ledger: %w", err)
	}
	return nil
}

func splitKey(k string) (string, string) {
	for i := 0; i < len(k); i++ {
		if k[i] == '/' {
			return k[:i], k[i+1:]
		}
	}
	return k, ""
}
