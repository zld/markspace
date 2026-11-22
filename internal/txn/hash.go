package txn

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
)

// rollingHash folds named byte chunks into one stable summary. Every Write
// folds as sha256(previous_state || chunk), so the order and content of
// chunks fully determines the result.
type rollingHash struct {
	h [32]byte
}

func newRollingHash() *rollingHash { return &rollingHash{} }

func (r *rollingHash) Write(b []byte) {
	cur := sha256.Sum256(append(r.h[:], b...))
	r.h = cur
}

func (r *rollingHash) Sum() string { return hex.EncodeToString(r.h[:]) }

// HashDirExport exposes the canonical directory hash used by the prepare and
// recovery protocol so external callers (the crash simulator) can produce a
// matching prepared marker.
func HashDirExported(dir string) (string, error) {
	return hashDir(dir)
}

// hashDir is the single canonical artifact hash used at prepare, commit and
// recovery time: every regular file under dir, in lexical relative-path
// order, contributes its relative path then its content hash.
func hashDir(dir string) (string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, rerr := filepath.Rel(dir, path)
			if rerr != nil {
				return rerr
			}
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := newRollingHash()
	for _, rel := range files {
		sum, err := HashFile(filepath.Join(dir, rel))
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel))
		h.Write([]byte(sum))
	}
	return h.Sum(), nil
}
