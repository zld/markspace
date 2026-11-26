package output

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediasync/internal/checkpoint"
	"mediasync/internal/media"
)

type VerifyReport struct {
	OK         bool     `json:"ok"`
	FileCount  int      `json:"file_count"`
	TotalBytes int64    `json:"total_bytes"`
	RootHash   string   `json:"root_hash"`
	Errors     []string `json:"errors,omitempty"`
}

func Verify(outDir string) (*Catalog, *VerifyReport, error) {
	c, _, err := LoadCatalog(outDir)
	if err != nil {
		return nil, &VerifyReport{OK: false}, err
	}
	report := &VerifyReport{RootHash: c.RootHash, OK: true}
	actual, err := scanFiles(outDir)
	if err != nil {
		return c, report, media.InputError("verify", "", err)
	}

	listed := make(map[string]FileEntry, len(c.Files))
	for _, entry := range c.Files {
		listed[entry.Path] = entry
		abs := filepath.Join(outDir, filepath.FromSlash(entry.Path))
		data, readErr := os.ReadFile(abs)
		if readErr != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: cannot read: %v", entry.Path, readErr))
			continue
		}
		if len(data) != entry.Bytes {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: bytes=%d catalog=%d", entry.Path, len(data), entry.Bytes))
		}
		sum := sha256.Sum256(data)
		gotHash := hex.EncodeToString(sum[:])
		if gotHash != entry.SHA256 {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: sha256 mismatch", entry.Path))
		}
		delete(actual, entry.Path)
	}

	extra := make([]string, 0, len(actual))
	for path := range actual {
		extra = append(extra, path)
	}
	sort.Strings(extra)
	for _, path := range extra {
		report.Errors = append(report.Errors, fmt.Sprintf("%s: unlisted file", path))
	}

	recomputedRoot := RootHash(c.Files)
	if recomputedRoot != c.RootHash {
		report.Errors = append(report.Errors, "root_hash does not match catalog entries")
	}
	if c.FileCount != len(c.Files) {
		report.Errors = append(report.Errors, fmt.Sprintf("file_count=%d but entries=%d", c.FileCount, len(c.Files)))
	}

	var total int64
	for _, f := range c.Files {
		total += int64(f.Bytes)
	}
	report.FileCount = len(c.Files)
	report.TotalBytes = total
	if total != c.TotalBytes {
		report.Errors = append(report.Errors, fmt.Sprintf("total_bytes=%d catalog=%d", total, c.TotalBytes))
	}
	if len(report.Errors) > 0 {
		report.OK = false
		return c, report, media.InputError("verify", "", fmt.Errorf("%s", strings.Join(report.Errors, "; ")))
	}
	return c, report, nil
}

func scanFiles(outDir string) (map[string]int64, error) {
	files := make(map[string]int64)
	err := filepath.WalkDir(outDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(outDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == CatalogName || rel == checkpoint.FileName {
			return nil
		}
		if strings.HasSuffix(rel, ".tmp") {
			return fmt.Errorf("temporary output remains: %s", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files[rel] = info.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

type Comparison struct {
	Equal bool   `json:"equal"`
	RootA string `json:"root_hash_a"`
	RootB string `json:"root_hash_b"`
}

func CompareDirs(a, b string) (*Comparison, error) {
	ca, _, err := LoadCatalog(a)
	if err != nil {
		return nil, err
	}
	cb, _, err := LoadCatalog(b)
	if err != nil {
		return nil, err
	}
	return &Comparison{Equal: ca.RootHash == cb.RootHash, RootA: ca.RootHash, RootB: cb.RootHash}, nil
}
