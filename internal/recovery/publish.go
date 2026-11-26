package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"path/filepath"
)

type sizeHasher struct {
	h hash.Hash
	n int64
}

func newSizeHasher() *sizeHasher { return &sizeHasher{h: sha256.New()} }

func (w *sizeHasher) Write(p []byte) (int, error) {
	n, err := w.h.Write(p)
	w.n += int64(n)
	return n, err
}

func (w *sizeHasher) SumHex() string { return hex.EncodeToString(w.h.Sum(nil)) }

// WriteTempSynced writes r to a unique temp file in targetDir and fsyncs it.
// The caller must append the prepared journal intent before Publish renames it.
func WriteTempSynced(targetDir, pattern string, r io.Reader) (string, string, int64, error) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", "", 0, err
	}
	f, err := os.CreateTemp(targetDir, pattern)
	if err != nil {
		return "", "", 0, err
	}
	tmp := f.Name()
	h := newSizeHasher()
	n, copyErr := io.Copy(io.MultiWriter(f, h), r)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return "", "", n, copyErr
	}
	if syncErr != nil {
		_ = os.Remove(tmp)
		return "", "", n, syncErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", "", n, closeErr
	}
	return tmp, h.SumHex(), n, nil
}

// Publish atomically renames tmp to target and fsyncs the directory.
func Publish(tmp, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	err = d.Sync()
	_ = d.Close()
	return err
}
