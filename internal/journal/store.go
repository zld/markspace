package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"continuity/internal/media"
)

func fault(code media.Code, source string, err error) *media.Fault {
	return &media.Fault{Phase: media.PhaseJournal, Code: code, Source: source, Err: err}
}

func appendJournal(dir, line string) error {
	p := filepath.Join(dir, journalFile)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fault(media.CodePublication, p, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, line); err != nil {
		return err
	}
	return f.Sync()
}

func atomicWrite(path string, data []byte, expectedSHA string) error {
	if expectedSHA == "" {
		sum := sha256.Sum256(data)
		expectedSHA = hex.EncodeToString(sum[:])
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Validate the durable temporary file before the atomic rename.
	tmpBytes, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	tmpSum := sha256.Sum256(tmpBytes)
	if got := hex.EncodeToString(tmpSum[:]); got != expectedSHA {
		return &media.Fault{Phase: media.PhaseJournal, Code: media.CodeHashMismatch, Source: name,
			Detail: fmt.Sprintf("temporary artifact hash %s != expected %s", got, expectedSHA)}
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
