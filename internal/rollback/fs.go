// Package rollback: atomic filesystem primitives.
package rollback

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"example.com/assetpack/internal/fault"
)

// writeFileAtomic writes data to a temp file in the same directory then
// renames it over the destination.
func writeFileAtomic(dst string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, filepath.Base(dst)+TempSuffix+"*")
	if err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "create temp for %s: %v", dst, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fault.New(fault.StageCommit, fault.KindIO, "write temp for %s: %v", dst, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fault.New(fault.StageCommit, fault.KindIO, "sync temp for %s: %v", dst, err)
	}
	if err := tmp.Close(); err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "close temp for %s: %v", dst, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "chmod temp for %s: %v", dst, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "rename temp over %s: %v", dst, err)
	}
	return nil
}

func renameAtomic(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "rename %s -> %s: %v", src, dst, err)
	}
	return nil
}

func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "open %s: %v", src, err)
	}
	defer in.Close()
	data, err := io.ReadAll(io.LimitReader(in, 64<<20))
	if err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "read %s: %v", src, err)
	}
	return writeFileAtomic(dst, data, 0o644)
}

func removeAndMkdir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "clear staging dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fault.New(fault.StageCommit, fault.KindIO, "create staging dir: %v", err)
	}
	return nil
}

// pruneArtifacts removes artifact files that the committed manifest does not
// reference.  Unreferenced files can only be leftovers from an interrupted
// promotion or a rollback, since every committed name is content-addressed.
func (c *Checkpoint) pruneArtifacts() error {
	m, err := c.ReadManifest()
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	if m != nil {
		for _, r := range m.Resources {
			keep[r.Artifact] = true
		}
	}
	entries, err := os.ReadDir(c.ArtifactDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fault.New(fault.StageRollback, fault.KindIO, "read artifact dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || keep[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(c.ArtifactDir(), e.Name())); err != nil {
			return fault.New(fault.StageRollback, fault.KindIO, "prune %s: %v", e.Name(), err)
		}
	}
	return nil
}

// HashFile returns the SHA-256 of a file's contents.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fault.New(fault.StageVerify, fault.KindIO, "open %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fault.New(fault.StageVerify, fault.KindIO, "hash %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// VerifyDir re-hashes every artifact named by the committed manifest.
func (c *Checkpoint) VerifyDir() error {
	m, err := c.ReadManifest()
	if err != nil {
		return err
	}
	if m == nil {
		return fault.New(fault.StageVerify, fault.KindMissing, "no committed manifest to verify")
	}
	digests := make(map[string]string, len(m.Resources))
	for _, r := range m.Resources {
		got, n, err := HashFile(filepath.Join(c.ArtifactDir(), r.Artifact))
		if err != nil {
			return err
		}
		if n != r.Bytes {
			return fault.New(fault.StageVerify, fault.KindDigest,
				"artifact %q has %d bytes, manifest says %d", r.Artifact, n, r.Bytes)
		}
		digests[r.Artifact] = got
	}
	return m.Verify(digests)
}
