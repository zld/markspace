package build

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"streamcheck/internal/fixture"
	"streamcheck/internal/m3u8"
	"streamcheck/internal/verify"
)

// segmentName renders the five-digit segment file naming convention shared by
// the slicer and the verifier's sequence extraction.
func segmentName(i int) string {
	return fmt.Sprintf("seg-%05d.ts", i)
}

// atomicWrite writes to a sibling temp file and renames it over the target,
// so readers never observe a partially written manifest.
func atomicWrite(path string, write func(*os.File) error) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := write(tmp); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// WriteMasterPlaylist emits the master playlist referencing every tier.
func WriteMasterPlaylist(outDir string, tiers []fixture.Tier) error {
	master := m3u8.MasterPlaylist{Version: 3}
	for _, t := range tiers {
		// Every path is a simple relative name confined to the output dir.
		master.Variants = append(master.Variants, m3u8.Variant{
			URI:       t.Name + "/index.m3u8",
			Bandwidth: t.BandwidthBPS,
			Codecs:    `"avc1.640028,mp4a.40.2"`,
		})
	}
	return atomicWrite(filepath.Join(outDir, "master.m3u8"), func(f *os.File) error {
		return m3u8.WriteMaster(f, master)
	})
}

// swapPlaylistEntries rewrites a media playlist exchanging the EXTINF+URI
// line pairs at two segment positions, modelling an out-of-order archive.
func swapPlaylistEntries(path string, a, b, numSegs int) error {
	if a < 0 || b < 0 || a >= numSegs || b >= numSegs {
		return fmt.Errorf("swap indices out of range: %d,%d", a, b)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	// Each segment occupies two lines: EXTINF then URI. Locate URI lines.
	var uriIdx []int
	for i, ln := range lines {
		if strings.HasPrefix(ln, "seg-") {
			uriIdx = append(uriIdx, i)
		}
	}
	if len(uriIdx) != numSegs {
		return errors.New("swap: playlist segment count mismatch")
	}
	ia, ib := uriIdx[a], uriIdx[b]
	// Swap both the EXTINF line above and the URI line to keep durations
	// attached to their entries while disordering the file sequence.
	for _, pair := range [][2]int{{ia - 1, ib - 1}, {ia, ib}} {
		lines[pair[0]], lines[pair[1]] = lines[pair[1]], lines[pair[0]]
	}
	return atomicWrite(path, func(f *os.File) error {
		w := bufio.NewWriter(f)
		if _, err := w.WriteString(strings.Join(lines, "\n")); err != nil {
			return err
		}
		return w.Flush()
	})
}

// validateAndPrepare checks the configuration and creates the output root,
// rejecting paths that escape the intended working area.
func validateAndPrepare(cfg Config) error {
	if len(cfg.Tiers) == 0 {
		return errors.New("build: at least one tier is required")
	}
	if cfg.Duration <= 0 || cfg.Segment <= 0 {
		return errors.New("build: duration and segment must be positive")
	}
	if cfg.Duration < cfg.Segment {
		return errors.New("build: duration must be at least one segment")
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	clean := filepath.Clean(cfg.OutDir)
	if filepath.IsAbs(clean) {
		// Absolute output paths are allowed but must not be a traversal root.
	} else if strings.HasPrefix(clean, "..") {
		return errors.New("build: output directory escapes the working area")
	}
	return os.MkdirAll(cfg.OutDir, 0o755)
}

func tierIndex(tiers []fixture.Tier, name string) int {
	for i, t := range tiers {
		if t.Name == name {
			return i
		}
	}
	return -1
}

// cleanupUnreferenced removes .ts files in a tier directory that are not part
// of the expected sequence. It runs only after the playlist has been
// atomically replaced, and deletion is confined to <outDir>/<tier>.
func cleanupUnreferenced(outDir string, tiers []fixture.Tier, numSegs int) error {
	referenced := make(map[string]bool, numSegs)
	for i := 0; i < numSegs; i++ {
		referenced[segmentName(i)] = true
	}
	for _, tier := range tiers {
		dir := filepath.Join(outDir, tier.Name)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, en := range entries {
			if en.IsDir() || filepath.Ext(en.Name()) != ".ts" {
				continue
			}
			if referenced[en.Name()] {
				continue
			}
			// Re-resolve and confirm the path stays inside the tier dir.
			target := filepath.Join(dir, en.Name())
			rel, err := filepath.Rel(dir, target)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errors.New("build: refusing cleanup outside tier directory")
			}
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func countSegments(results []verify.TierResult) map[string]int {
	out := make(map[string]int, len(results))
	for _, r := range results {
		out[r.Tier] = r.Segments
	}
	return out
}

// keep time imported for callers that construct durations.
var _ = time.Second
