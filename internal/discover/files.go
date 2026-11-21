// Package discover scans an input directory and normalizes segment file names
// into a stable, numerically ordered view.
package discover

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"streamcheck/internal/manifest"
)

// Options controls a scan.
type Options struct {
	// Pattern is the compiled segment naming rule.
	Pattern *manifest.Pattern
	// RateFilter, when non-empty, keeps only these bitrates.
	RateFilter []int
}

// Result is the outcome of a scan. Problems are recoverable and are meant to
// be reported; Err is returned only when the directory itself cannot be read.
type Result struct {
	// Segments holds one entry per unique rate and segment id.
	Segments []manifest.Segment
	// Files holds every matched regular file, including duplicates. It is
	// used to detect files that change after scanning.
	Files []manifest.Segment
	// Duplicates maps rate and segment id to every conflicting file path.
	Duplicates map[int]map[int][]string
	// Ignored counts files whose names do not follow the pattern.
	Ignored int
	// Playlists counts .m3u8/.m3u files encountered in the input directory.
	Playlists int
	// OtherRates lists rates found on disk that were not requested.
	OtherRates []int
}

// Scan reads dir without following subdirectories or symlinks and groups
// files by rate. Enumeration order never matters: the result is sorted by
// rate then segment id.
func Scan(dir string, opts Options) (Result, error) {
	res := Result{Duplicates: map[int]map[int][]string{}}
	info, err := os.Stat(dir)
	if err != nil {
		return res, fmt.Errorf("input directory: %w", err)
	}
	if !info.IsDir() {
		return res, fmt.Errorf("input path %q is not a directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return res, fmt.Errorf("read input directory: %w", err)
	}

	wanted := map[int]bool{}
	for _, r := range opts.RateFilter {
		wanted[r] = true
	}
	otherRates := map[int]bool{}
	type key struct{ rate, id int }
	byID := map[key][]manifest.Segment{}

	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if name == "" || name[0] == '.' {
			res.Ignored++
			continue
		}
		if strings.HasSuffix(name, ".m3u8") || strings.HasSuffix(name, ".m3u") {
			// Playlists are this tool's own output, not source segments.
			res.Playlists++
			continue
		}
		rate, id, ok := opts.Pattern.Match(name)
		if !ok {
			res.Ignored++
			continue
		}
		if rate >= 0 && len(wanted) > 0 && !wanted[rate] {
			otherRates[rate] = true
			continue
		}
		fi, err := entry.Info()
		if err != nil {
			return res, fmt.Errorf("stat %q: %w", name, err)
		}
		if !fi.Mode().IsRegular() {
			res.Ignored++
			continue
		}
		abs, err := filepath.Abs(filepath.Join(dir, name))
		if err != nil {
			return res, fmt.Errorf("resolve %q: %w", name, err)
		}
		seg := manifest.Segment{
			ID:              id,
			RateKbps:        rate,
			Path:            abs,
			FileName:        name,
			SizeBytes:       fi.Size(),
			ModTimeUnixNano: fi.ModTime().UnixNano(),
		}
		res.Files = append(res.Files, seg)
		byID[key{rate: rate, id: id}] = append(byID[key{rate: rate, id: id}], seg)
	}

	for k, group := range byID {
		sort.Slice(group, func(i, j int) bool { return group[i].FileName < group[j].FileName })
		if len(group) > 1 {
			names := make([]string, len(group))
			for i, seg := range group {
				names[i] = seg.Path
			}
			if res.Duplicates[k.rate] == nil {
				res.Duplicates[k.rate] = map[int][]string{}
			}
			res.Duplicates[k.rate][k.id] = names
			continue
		}
		res.Segments = append(res.Segments, group[0])
	}
	sort.Slice(res.Files, func(i, j int) bool {
		if res.Files[i].RateKbps != res.Files[j].RateKbps {
			return res.Files[i].RateKbps < res.Files[j].RateKbps
		}
		if res.Files[i].ID != res.Files[j].ID {
			return res.Files[i].ID < res.Files[j].ID
		}
		return res.Files[i].FileName < res.Files[j].FileName
	})
	sort.Slice(res.Segments, func(i, j int) bool {
		if res.Segments[i].RateKbps != res.Segments[j].RateKbps {
			return res.Segments[i].RateKbps < res.Segments[j].RateKbps
		}
		if res.Segments[i].ID != res.Segments[j].ID {
			return res.Segments[i].ID < res.Segments[j].ID
		}
		return res.Segments[i].FileName < res.Segments[j].FileName
	})
	for rate := range otherRates {
		res.OtherRates = append(res.OtherRates, rate)
	}
	sort.Ints(res.OtherRates)
	return res, nil
}

// DuplicateIDs returns the sorted ids with conflicting files for a rate.
func (r Result) DuplicateIDs(rate int) []int {
	byID := r.Duplicates[rate]
	ids := make([]int, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}
