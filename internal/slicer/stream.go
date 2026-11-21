// Package slicer turns ordered-but-unreliable source TS segments into
// timeline-correct HLS segments. It reorders by filename sequence, waits on
// gaps for a configured window, only cuts on H.264 IDR boundaries and marks
// discontinuities instead of silently joining across missing source data.
package slicer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SegmentNamePattern extracts the numeric sequence from a source file name.
// Accepted forms: seg-00005.ts, seg-5.ts, 5.ts.
var segmentNamePattern = func() func(string) (int, bool) {
	return func(name string) (int, bool) {
		base := filepath.Base(name)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		if i := strings.LastIndex(base, "-"); i >= 0 {
			base = base[i+1:]
		}
		var n int
		if _, err := fmt.Sscanf(base, "%d", &n); err != nil {
			return 0, false
		}
		return n, true
	}
}()

// SourceFile is one discovered input segment.
type SourceFile struct {
	Seq  int
	Path string
}

// Discover lists and sequence-validates TS files beneath dir. Files that do not
// match the naming pattern are returned as rejects rather than dropped.
func Discover(dir string) (files []SourceFile, rejects []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("discover %q: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		seq, ok := segmentNamePattern(e.Name())
		if !ok {
			rejects = append(rejects, e.Name())
			continue
		}
		files = append(files, SourceFile{Seq: seq, Path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Seq < files[j].Seq })
	return files, rejects, nil
}

// ReorderedCount compares the on-disk arrival order (readdir order) against
// the sorted sequence order and counts how many files moved.
func ReorderedCount(arrival []int, sorted []SourceFile) int {
	if len(arrival) != len(sorted) {
		return 0
	}
	pos := make(map[int]int, len(arrival))
	for i, seq := range arrival {
		pos[seq] = i
	}
	count := 0
	for i, sf := range sorted {
		if pos[sf.Seq] != i {
			count++
		}
	}
	return count
}

// WaitForGap polls dir until seq appears or the timeout elapses. It returns
// true when the file showed up. A zero timeout performs no waiting.
func WaitForGap(dir string, seq int, timeout time.Duration) bool {
	if timeout <= 0 {
		return false
	}
	deadline := time.Now().Add(timeout)
	name := fmt.Sprintf("seg-%05d.ts", seq)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

