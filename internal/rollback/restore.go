// Package rollback restores a published set of artifacts to a previously
// recorded version. Every version has an immutable manifest naming each
// artifact together with its source and derived digests; rollback only moves
// the "current" reference and appends an auditable transition, never
// rewriting artifact bytes.
package rollback

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"media-check/internal/assets"
	"media-check/internal/publish"
)

// ErrUnknownVersion is returned when no manifest exists for a target.
var ErrUnknownVersion = errors.New("rollback: unknown target version")

// ManifestItem pins one artifact by name and content digest.
type ManifestItem struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Digest      string `json:"digest"`
	SourceDigest string `json:"source_digest"`
}

// Manifest is the immutable, auditable description of a published version.
type Manifest struct {
	Version      string         `json:"version"`
	SourceDigest string         `json:"source_digest"`
	Items        []ManifestItem `json:"items"`
	CreatedAt    time.Time      `json:"created_at"`
}

// AuditEntry records one publish/rollback state transition.
type AuditEntry struct {
	Time         time.Time `json:"time"`
	Action       string    `json:"action"`
	FromVersion  string    `json:"from_version,omitempty"`
	ToVersion    string    `json:"to_version"`
	SourceDigest string    `json:"source_digest"`
	ManifestDigest string  `json:"manifest_digest"`
}

// Manager owns manifests, the current-version pointer and the audit trail.
type Manager struct {
	mu  sync.Mutex
	dir string
}

// NewManager opens (or creates) the rollback store in dir.
func NewManager(dir string) (*Manager, error) {
	for _, sub := range []string{"manifests", "audit"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("rollback: create %s: %w", sub, err)
		}
	}
	return &Manager{dir: dir}, nil
}

// Snapshot builds and persists an immutable manifest from a version's
// committed ledger artifacts.
func (m *Manager) Snapshot(version, sourceDigest string, ledger *publish.Ledger) (*Manifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	committed := ledger.Committed()
	mf := &Manifest{
		Version: version, SourceDigest: sourceDigest, CreatedAt: time.Now().UTC(),
	}
	for _, a := range committed {
		if a.Version != version {
			continue
		}
		mf.Items = append(mf.Items, ManifestItem{
			Name: a.Name, Path: a.Path, Digest: a.Digest, SourceDigest: a.SourceDigest,
		})
	}
	if len(mf.Items) == 0 {
		return nil, fmt.Errorf("rollback: version %s has no committed artifacts", version)
	}
	if err := m.writeManifest(mf); err != nil {
		return nil, err
	}
	m.appendAuditLocked(AuditEntry{
		Action: "snapshot", ToVersion: version, SourceDigest: sourceDigest,
		ManifestDigest: Digest(mf),
	})
	return mf, nil
}

// Rollback repoints current at an existing, verified target manifest.
func (m *Manager) Rollback(target string, ledger *publish.Ledger) (*Manifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mf, err := m.readManifest(target)
	if err != nil {
		return nil, err
	}
	// Every pinned artifact must still verify before we move the pointer.
	for _, it := range mf.Items {
		a, ok := ledger.Find(target, it.Name)
		if !ok || a.Digest != it.Digest {
			return nil, fmt.Errorf("%w: artifact %s missing or digest drift",
				ErrUnknownVersion, it.Name)
		}
	}
	// Capture the previous pointer before overwriting it, so the audit trail
	// records a real from->to transition.
	prev, _ := m.currentVersionLocked()
	if err := m.writeCurrent(mf); err != nil {
		return nil, err
	}
	m.appendAuditLocked(AuditEntry{
		Action: "rollback", FromVersion: prev, ToVersion: target,
		SourceDigest: mf.SourceDigest, ManifestDigest: Digest(mf),
	})
	return mf, nil
}

// Current returns the manifest currently pointed at, if any.
func (m *Manager) Current() (*Manifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := os.ReadFile(m.currentPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var mf Manifest
	if err := json.Unmarshal(b, &mf); err != nil {
		return nil, fmt.Errorf("rollback: corrupt current pointer: %w", err)
	}
	return &mf, nil
}

// Audit returns the full transition trail in append order.
func (m *Manager) Audit() ([]AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readAuditLocked()
}

func (m *Manager) manifestPath(v string) string {
	return filepath.Join(m.dir, "manifests", v+".json")
}
func (m *Manager) currentPath() string { return filepath.Join(m.dir, "current.json") }
func (m *Manager) auditPath() string   { return filepath.Join(m.dir, "audit", "transitions.jsonl") }

func (m *Manager) writeManifest(mf *Manifest) error {
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.manifestPath(mf.Version) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.manifestPath(mf.Version))
}

func (m *Manager) readManifest(v string) (*Manifest, error) {
	b, err := os.ReadFile(m.manifestPath(v))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrUnknownVersion, v)
	}
	if err != nil {
		return nil, err
	}
	var mf Manifest
	if err := json.Unmarshal(b, &mf); err != nil {
		return nil, fmt.Errorf("rollback: corrupt manifest %s: %w", v, err)
	}
	return &mf, nil
}

func (m *Manager) writeCurrent(mf *Manifest) error {
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.currentPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.currentPath())
}

func (m *Manager) currentVersionLocked() (string, error) {
	b, err := os.ReadFile(m.currentPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var mf Manifest
	return mf.Version, json.Unmarshal(b, &mf)
}

func (m *Manager) appendAuditLocked(e AuditEntry) error {
	e.Time = time.Now().UTC()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(m.auditPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (m *Manager) readAuditLocked() ([]AuditEntry, error) {
	b, err := os.ReadFile(m.auditPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []AuditEntry
	for _, line := range splitLines(b) {
		if len(line) == 0 {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("rollback: corrupt audit line: %w", err)
		}
		out = append(out, e)
	}
	return out, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// Digest is the content digest of a manifest, used in audit and assertions.
func Digest(mf *Manifest) string {
	b, _ := json.Marshal(mf)
	return assets.Digest(b)
}
