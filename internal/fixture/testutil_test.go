package fixture

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func dirsEqual(t *testing.T, a, b string) bool {
	t.Helper()
	entriesA, err := os.ReadDir(a)
	if err != nil {
		t.Fatal(err)
	}
	entriesB, err := os.ReadDir(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesA) != len(entriesB) {
		return false
	}
	for i := range entriesA {
		if entriesA[i].Name() != entriesB[i].Name() {
			return false
		}
		ha := hashFile(t, filepath.Join(a, entriesA[i].Name()))
		hb := hashFile(t, filepath.Join(b, entriesB[i].Name()))
		if ha != hb {
			return false
		}
	}
	return true
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return string(h.Sum(nil))
}
