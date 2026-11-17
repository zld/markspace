package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"subtitle/internal/report"
)

// atomicWrite writes data to a sibling temporary file then renames it into
// place. Existing outputs are refused unless force is set, and the input file
// itself can never be overwritten.
func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	if absA == absB {
		return true
	}
	infoA, errA := os.Stat(absA)
	infoB, errB := os.Stat(absB)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(infoA, infoB)
}

// atomicWrite writes data to a sibling temporary file then renames it into
// place. Existing outputs are refused unless force is set.
func atomicWrite(path string, data []byte, force bool) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".subtitle-*."+filepath.Base(path))
	if err != nil {
		return fmt.Errorf("create temp output: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", path, err)
	}
	if _, err := os.Stat(path); err == nil {
		if !force {
			cleanup()
			return fmt.Errorf("output %s already exists; pass --force to overwrite", path)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename into %s: %w", path, err)
	}
	return nil
}

// supportedExt lists extensions picked up by directory batch runs.
var supportedExt = map[string]bool{".srt": true, ".vtt": true, ".ass": true}

// DiscoverInputs returns supported files under dir in sorted order.
func DiscoverInputs(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if supportedExt[filepath.Ext(path)] {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	sort.Strings(paths)
	return paths, nil
}

// BatchOptions extends Options with concurrency settings for directory runs.
type BatchOptions struct {
	Options
	Jobs int
}

// Batch converts every input path with at most jobs concurrent workers. One
// file failing never aborts the others; results come back sorted by path.
func Batch(paths []string, jobs int, base Options) []report.FileResult {
	if jobs < 1 {
		jobs = 1
	}
	if jobs > len(paths) {
		jobs = len(paths)
	}
	type indexed struct {
		idx int
		res report.FileResult
	}
	ch := make(chan indexed, len(paths))
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func(idx int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			opts := base
			opts.InputPath = path
			ch <- indexed{idx: idx, res: Run(opts)}
		}(i, p)
	}
	wg.Wait()
	close(ch)

	results := make([]report.FileResult, len(paths))
	for item := range ch {
		results[item.idx] = item.res
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Path < results[j].Path })
	return results
}
