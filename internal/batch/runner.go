// Package batch schedules single-file analysis across a set of WAV files and
// collects the results in a stable order.
//
// Concurrency only changes when work happens, never what it produces: each
// worker builds its own analyzer and verdict, and results are sorted by path
// before being returned.
package batch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"loudcheck/internal/loudness"
	"loudcheck/internal/policy"
	"loudcheck/internal/wav"
)

// FrameBlock is the number of frames decoded per read, bounding each
// worker's buffer to roughly one second of 32-bit audio.
const FrameBlock = 8192

// FileReport is the outcome for one file. Exactly one of Err or a complete
// measurement is set; a file that failed to parse never carries numbers.
type FileReport struct {
	Path     string
	Layout   string
	Format   string
	Info     wav.Info
	Channels []string
	Measure  loudness.Result
	Verdict  policy.Verdict
	Err      string
}

// Summary aggregates a batch run.
type Summary struct {
	Results []FileReport
	// Failed counts files whose analysis aborted with an error.
	Failed int
	// Passed counts files that measured and met every threshold.
	Passed int
	// NotPassed counts files that measured but missed a threshold.
	NotPassed int
}

// AllPassed reports whether every file measured and met every threshold.
func (s Summary) AllPassed() bool {
	return s.Failed == 0 && s.NotPassed == 0 && s.Passed == len(s.Results)
}

// Options selects layouts and thresholds for a run.
type Options struct {
	LayoutName string
	Mapping    string
	Thresholds policy.Thresholds
	Workers    int
}

// AnalyzeFiles analyses each path and returns results sorted by path.
func AnalyzeFiles(paths []string, opts Options) Summary {
	unique := dedupe(paths)
	sort.Strings(unique)
	workers := opts.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > len(unique) {
		workers = len(unique)
	}
	if workers > 64 {
		workers = 64
	}

	out := make([]FileReport, len(unique))
	if workers == 0 {
		return Summary{}
	}
	index := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range index {
				out[i] = analyzeOne(unique[i], opts)
			}
		}()
	}
	for i := range unique {
		index <- i
	}
	close(index)
	wg.Wait()

	s := Summary{Results: out}
	for _, r := range out {
		switch {
		case r.Err != "":
			s.Failed++
		case r.Verdict.Pass:
			s.Passed++
		default:
			s.NotPassed++
		}
	}
	return s
}

func dedupe(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func analyzeOne(path string, opts Options) FileReport {
	rep := FileReport{Path: path}
	r, err := wav.Open(path)
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	defer r.Close()

	rep.Info = r.Info
	rep.Format = formatName(r.Info)
	layout, err := loudness.Resolve(opts.LayoutName, r.Info.Channels, opts.Mapping)
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	rep.Layout = layout.Name
	rep.Channels = layout.ChannelNames

	an := loudness.NewAnalyzer(r.Info.SampleRate, layout.Weights)
	buf := make([]float32, FrameBlock*r.Info.Channels)
	for {
		n, err := r.ReadFrames(buf)
		if n > 0 {
			an.Write(buf[:n*r.Info.Channels])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			rep.Err = err.Error()
			return rep
		}
	}
	rep.Measure = an.Finish()
	rep.Verdict = policy.Evaluate(rep.Measure, opts.Thresholds)
	return rep
}

func formatName(info wav.Info) string {
	kind := "pcm"
	if info.Format == wav.FormatFloat {
		kind = "float"
	}
	return fmt.Sprintf("%s%d/%dch/%dHz", kind, info.Bits, info.Channels, info.SampleRate)
}

// ExpandPath returns the WAV files under path. For a directory it scans one
// level deep in lexical order; for a file it returns that file. Only local
// paths are accepted: anything that looks like a URL is rejected.
func ExpandPath(path string) ([]string, error) {
	if strings.Contains(path, "://") {
		return nil, fmt.Errorf("batch: %s is not a local path", path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().IsRegular() {
		return []string{path}, nil
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("batch: %s is not a regular file or directory", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(e.Name()), ".wav") {
			continue
		}
		files = append(files, filepath.Join(path, e.Name()))
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("batch: no .wav files in %s", path)
	}
	return files, nil
}

// ErrNoInput is returned when no input paths were supplied.
var ErrNoInput = errors.New("batch: no input files")
