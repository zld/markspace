package job

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// maxMemoryChunk bounds the verification read buffer, honoring the 64 MiB
// streaming memory ceiling (chunks are far smaller).
const maxMemoryChunk = 1 << 20 // 1 MiB

// Archive is a content-addressed object store rooted at a directory. Object
// paths are sharded by the first two hash bytes to avoid flat directories.
type Archive struct {
	root string
}

// OpenArchive ensures the store directory exists.
func OpenArchive(root string) (*Archive, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Archive{root: root}, nil
}

// objectPath maps a hex hash to its sharded path.
func (a *Archive) objectPath(hash string) (string, error) {
	if len(hash) != 64 {
		return "", fmt.Errorf("archive: object hash must be 64 hex chars, got %d", len(hash))
	}
	dir := filepath.Join(a.root, hash[0:2], hash[2:4])
	return filepath.Join(dir, hash), nil
}

// Put streams content to a temporary file, hashes it in bounded chunks, then
// atomically renames it into place. It returns the hash and whether a new
// object was written: re-submitting identical content reports written=false
// so callers can count duplicate writes.
func (a *Archive) Put(content []byte) (hash string, written bool, err error) {
	sum := sha256.Sum256(content)
	hash = hex.EncodeToString(sum[:])
	final, err := a.objectPath(hash)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(final); err == nil {
		// A path already exists, but only trust it if the content still
		// hashes correctly. A corrupted object is removed and rewritten so
		// content addressing never silently serves bad bytes.
		if verifyErr := a.Verify(hash); verifyErr == nil {
			return hash, false, nil
		}
		if err := os.Remove(final); err != nil {
			return "", false, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", false, err
	}
	tmp, err := os.CreateTemp(a.root, ".tmp-*")
	if err != nil {
		return "", false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	h := sha256.New()
	if _, err := io.CopyBuffer(h, bytes.NewReader(content), make([]byte, maxMemoryChunk)); err != nil {
		tmp.Close()
		return "", false, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != hash {
		tmp.Close()
		return "", false, errors.New("archive: streaming hash mismatch before commit")
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return "", false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", false, err
	}
	return hash, true, nil
}

// Verify re-hashes an archived object chunk by chunk and compares it to the
// expected content hash. A mismatch or missing object returns a StageRecover
// structured error so callers never publish a manifest over corrupt data.
func (a *Archive) Verify(expectedHash string) error {
	path, err := a.objectPath(expectedHash)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return recoverError("archive.open", err.Error(), path)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, maxMemoryChunk)); err != nil {
		return recoverError("archive.read", err.Error(), path)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != expectedHash {
		return recoverError("archive.verify",
			fmt.Sprintf("hash mismatch: expected %s got %s", expectedHash, got), path)
	}
	return nil
}

// Root exposes the store root for diagnostics.
func (a *Archive) Root() string { return a.root }

func recoverError(op, reason, path string) error {
	return &recoverFailure{op: op, reason: reason, path: path}
}

type recoverFailure struct {
	op     string
	reason string
	path   string
}

func (e *recoverFailure) Error() string {
	return "recover: " + e.op + ": " + e.reason + " path=" + e.path
}
