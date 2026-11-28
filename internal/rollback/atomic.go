// Package rollback manages release versions and the atomic active
// pointer. Publishing writes the new version's files plus a checksum
// record first, then switches the pointer with an atomic rename.
// After an interruption the store recovers to a complete old or a
// complete new version only — never a mixed state.
package rollback

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediacheck/internal/fault"
)

const checksumFile = "checksums.txt"

// Store is the root of the versioned release area.
type Store struct {
	Root string
}

func (s *Store) releasesDir() string { return filepath.Join(s.Root, "releases") }
func (s *Store) pointerPath() string { return filepath.Join(s.Root, "active") }

// Release is a version under construction.
type Release struct {
	name string
	dir  string
}

// Begin creates the directory for a new version.
func (s *Store) Begin(name string) (*Release, error) {
	dir := filepath.Join(s.releasesDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Release{name: name, dir: dir}, nil
}

// Name returns the version name.
func (r *Release) Name() string { return r.name }

// Write stores one artifact inside the release.
func (r *Release) Write(name string, data []byte) error {
	if strings.ContainsRune(name, '/') || name == checksumFile {
		return fmt.Errorf("invalid release file name %q", name)
	}
	return os.WriteFile(filepath.Join(r.dir, name), data, 0o644)
}

// Finalize writes the checksum record covering every artifact.
func (r *Release) Finalize() error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	var lines []string
	for _, e := range entries {
		if e.IsDir() || e.Name() == checksumFile {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, e.Name()))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+e.Name())
	}
	sort.Strings(lines)
	return os.WriteFile(filepath.Join(r.dir, checksumFile),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// Switch atomically points the store at the named version: the
// pointer is written to a temporary file and renamed into place.
func (s *Store) Switch(name string) error {
	tmp := s.pointerPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(name+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.pointerPath())
}

// Active returns the currently active version, or "" if none.
func (s *Store) Active() (string, error) {
	data, err := os.ReadFile(s.pointerPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Verify checks a version's checksum record against its files.
func (s *Store) Verify(name string) error {
	dir := filepath.Join(s.releasesDir(), name)
	data, err := os.ReadFile(filepath.Join(dir, checksumFile))
	if err != nil {
		return fault.New(fault.ErrIntegrity, fault.NoOffset,
			"release %s has no checksum record", name)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			return fault.New(fault.ErrIntegrity, fault.NoOffset, "release %s: bad checksum line", name)
		}
		body, err := os.ReadFile(filepath.Join(dir, parts[1]))
		if err != nil {
			return fault.New(fault.ErrIntegrity, fault.NoOffset,
				"release %s: missing file %s", name, parts[1])
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != parts[0] {
			return fault.New(fault.ErrIntegrity, fault.NoOffset,
				"release %s: checksum mismatch for %s", name, parts[1])
		}
	}
	return nil
}

// Report describes the recovered store state.
type Report struct {
	Active       string   // active version after recovery ("" if none)
	ActiveIntact bool     // active version passed checksum verification
	Removed      []string // incomplete versions cleaned up
}

// Recover inspects the store after a possible interruption: the
// active version must verify completely, and any incomplete leftover
// version is removed. The result is exactly one active version that
// is either the complete old or the complete new release.
func (s *Store) Recover() (*Report, error) {
	rep := &Report{}
	active, err := s.Active()
	if err != nil {
		return nil, err
	}
	rep.Active = active
	if active != "" {
		if err := s.Verify(active); err != nil {
			return rep, err
		}
		rep.ActiveIntact = true
	}
	entries, err := os.ReadDir(s.releasesDir())
	if os.IsNotExist(err) {
		return rep, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == active {
			continue
		}
		if err := s.Verify(e.Name()); err != nil {
			os.RemoveAll(filepath.Join(s.releasesDir(), e.Name()))
			rep.Removed = append(rep.Removed, e.Name())
		}
	}
	return rep, nil
}
