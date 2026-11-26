package output

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediasync/internal/checkpoint"
	"mediasync/internal/media"
	"mediasync/internal/render"
	"mediasync/internal/timeline"
)

const CatalogName = "manifest.json"

type FileEntry struct {
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Budget struct {
	BudgetUnits int64 `json:"budget_units"`
	UsedUnits   int64 `json:"used_units"`
	MaxBytes    int64 `json:"max_bytes"`
	TotalBytes  int64 `json:"total_bytes"`
}

type Catalog struct {
	Schema     string                 `json:"schema"`
	State      checkpoint.State       `json:"state"`
	RootHash   string                 `json:"root_hash"`
	FileCount  int                    `json:"file_count"`
	TotalBytes int64                  `json:"total_bytes"`
	Stats      timeline.Stats         `json:"stats"`
	Quality    timeline.QualityReport `json:"quality"`
	Budget     Budget                 `json:"budget"`
	Frames     int                    `json:"frames"`
	Files      []FileEntry            `json:"files"`
}

// WriteTimeline writes deterministic JSON and returns its catalog entry.
func WriteTimeline(outDir string, tl *timeline.Timeline) (FileEntry, error) {
	data, err := json.MarshalIndent(tl, "", "  ")
	if err != nil {
		return FileEntry{}, err
	}
	data = append(data, '\n')
	rel := "timeline.json"
	if err := atomicWrite(filepath.Join(outDir, rel), data); err != nil {
		return FileEntry{}, media.InputError("output.timeline", "", err)
	}
	return EntryFromBytes(rel, data), nil
}

func EntryFromBytes(rel string, data []byte) FileEntry {
	sum := sha256.Sum256(data)
	return FileEntry{Path: rel, Bytes: len(data), SHA256: hex.EncodeToString(sum[:])}
}

func EntryFromRender(r render.Result) FileEntry {
	return FileEntry{Path: r.Path, Bytes: r.Bytes, SHA256: r.SHA256}
}

// RootHash computes a stable hash over path, size and item SHA-256.
func RootHash(files []FileEntry) string {
	sorted := append([]FileEntry(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		fmt.Fprintf(h, "%s:%d:%s\n", f.Path, f.Bytes, f.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func BuildCatalog(tl *timeline.Timeline, frames []render.Result, files []FileEntry, budget Budget) *Catalog {
	all := append([]FileEntry(nil), files...)
	for _, fr := range frames {
		all = append(all, EntryFromRender(fr))
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })
	var total int64
	for _, f := range all {
		total += int64(f.Bytes)
	}
	budget.TotalBytes = total
	return &Catalog{
		Schema: "mediasync-catalog/v1", State: checkpoint.StateCommitted,
		RootHash: RootHash(all), FileCount: len(all), TotalBytes: total,
		Stats: tl.Stats, Quality: tl.Quality, Budget: budget,
		Frames: len(frames), Files: all,
	}
}

func SaveCatalog(outDir string, c *Catalog) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicWrite(filepath.Join(outDir, CatalogName), data); err != nil {
		return media.InputError("output.catalog", "", err)
	}
	return nil
}

func LoadCatalog(outDir string) (*Catalog, string, error) {
	path := filepath.Join(outDir, CatalogName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, path, media.InputError("verify", "", err)
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, path, media.InputError("verify", "", fmt.Errorf("parse catalog: %w", err)).WithOffset(0)
	}
	return &c, path, nil
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func IsCommitted(outDir string) (bool, error) {
	c, _, err := LoadCatalog(outDir)
	if err != nil {
		if fe := media.AsError(err); fe != nil {
			if os.IsNotExist(fe.Unwrap()) || strings.Contains(fe.Error(), "no such file") {
				return false, nil
			}
		}
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return c.State == checkpoint.StateCommitted, nil
}
