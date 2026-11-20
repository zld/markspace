// Package batch runs subtitle conversions for single files and directories.
package batch

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"subx/internal/charset"
	"subx/internal/cue"
	"subx/internal/format"
	"subx/internal/report"
	"subx/internal/timeline"
	"subx/internal/verify"
)

// Limits from the delivery constraints.
const (
	MaxFileBytes = 64 << 20 // 64 MiB
	MaxCues      = 500000
)

// Exit code categories.
const (
	CodeOK        = 0
	CodeIO        = 2 // parameter or file read/write error
	CodeFormat    = 3 // subtitle format or encoding error
	CodeRoundTrip = 4 // serialize/re-parse inconsistency
)

// Options carries the validated conversion parameters.
type Options struct {
	From     format.Format // empty: infer from input extension
	To       format.Format
	FPS      timeline.FPS
	Overlap  timeline.Policy
	Encoding string // empty: sniff
	Jobs     int
}

// Job is one input/output file pair.
type Job struct {
	Input  string
	Output string
}

// ConvertOne converts a single file and verifies the round trip.
func ConvertOne(j Job, o Options) report.FileReport {
	rep := report.FileReport{Input: j.Input, Output: j.Output, Status: "ok"}
	fail := func(code int, err error) report.FileReport {
		rep.Status, rep.Error, rep.Code = "error", err.Error(), code
		return rep
	}
	if samePath(j.Input, j.Output) {
		return fail(CodeIO, fmt.Errorf("input and output are the same path: %s", j.Input))
	}
	info, err := os.Stat(j.Input)
	if err != nil {
		return fail(CodeIO, fmt.Errorf("stat input: %v", err))
	}
	if info.Size() > MaxFileBytes {
		return fail(CodeIO, fmt.Errorf("input exceeds 64 MiB limit: %d bytes", info.Size()))
	}
	data, err := os.ReadFile(j.Input)
	if err != nil {
		return fail(CodeIO, fmt.Errorf("read input: %v", err))
	}
	text, _, err := charset.Decode(data, o.Encoding)
	if err != nil {
		return fail(CodeFormat, err)
	}
	from := o.From
	if from == "" {
		if from = formatForPath(j.Input); from == "" {
			return fail(CodeIO, fmt.Errorf("cannot infer input format from %s; pass --from", j.Input))
		}
	}
	cues, err := format.Parse(from, text, j.Input)
	if err != nil {
		return fail(CodeFormat, err)
	}
	if len(cues) > MaxCues {
		return fail(CodeFormat, fmt.Errorf("input has %d cues, limit is %d", len(cues), MaxCues))
	}
	kept, dropped, err := timeline.Resolve(timeline.Map(cues, o.FPS), o.Overlap)
	if err != nil {
		return fail(CodeFormat, err)
	}
	outCues := make([]cue.Cue, len(kept))
	for i, fc := range kept {
		outCues[i] = cue.Cue{ID: fc.ID, Settings: fc.Settings, Lines: fc.Lines,
			StartMs: o.FPS.FrameStartMs(fc.StartFrame), EndMs: o.FPS.FrameEndMs(fc.EndFrame)}
	}
	serialized, err := format.Serialize(o.To, outCues)
	if err != nil {
		return fail(CodeIO, err)
	}
	results, maxDelta, err := verify.Check(serialized, o.To, kept, o.FPS, j.Output)
	if err != nil {
		return fail(CodeRoundTrip, err)
	}
	if err := WriteFileAtomic(j.Output, serialized); err != nil {
		return fail(CodeIO, fmt.Errorf("write output: %v", err))
	}
	rep.InputCount, rep.OutputCount = len(cues), len(kept)
	rep.DroppedCount, rep.MaxAbsDeltaMs = len(dropped), maxDelta
	for i, fc := range kept {
		d := results[i].DeltaMs
		status := "ok"
		if fc.Trimmed {
			status = "trimmed"
		}
		rep.Cues = append(rep.Cues, report.CueLine{CueID: fc.ID, StartFrame: fc.StartFrame,
			EndFrameExclusive: fc.EndFrame, RoundtripDeltaMs: &d, Status: status})
	}
	for _, fc := range dropped {
		rep.Cues = append(rep.Cues, report.CueLine{CueID: fc.ID, StartFrame: fc.StartFrame,
			EndFrameExclusive: fc.EndFrame, Status: "dropped"})
	}
	return rep
}

// Plan lists convertible inputs in inputDir and pairs them with outputs in
// outputDir. Output path conflicts are detected before anything is written.
func Plan(inputDir, outputDir string, to format.Format) ([]Job, error) {
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		return nil, fmt.Errorf("read input dir: %v", err)
	}
	outAbs, err := filepath.Abs(outputDir)
	if err != nil {
		return nil, err
	}
	var jobs []Job
	seen := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || formatForPath(e.Name()) == "" {
			continue
		}
		in := filepath.Join(inputDir, e.Name())
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())) + "." + string(to)
		out := filepath.Join(outputDir, base)
		abs, err := filepath.Abs(out)
		if err != nil {
			return nil, err
		}
		if rel, err := filepath.Rel(outAbs, abs); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("output path %s escapes target directory %s", out, outputDir)
		}
		if prev, dup := seen[abs]; dup {
			return nil, fmt.Errorf("output conflict: %s and %s both map to %s", prev, in, out)
		}
		seen[abs] = in
		if samePath(in, out) {
			return nil, fmt.Errorf("input and output are the same path: %s", in)
		}
		jobs = append(jobs, Job{Input: in, Output: out})
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("no .srt or .vtt files found in %s", inputDir)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Input < jobs[j].Input })
	return jobs, nil
}

// Run executes jobs with bounded concurrency; reports come back sorted by
// input path. A failing file never blocks or corrupts the others.
func Run(jobs []Job, o Options) []report.FileReport {
	workers := o.Jobs
	if workers < 1 {
		workers = 1
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	reps := make([]report.FileReport, len(jobs))
	var wg sync.WaitGroup
	ch := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				reps[i] = ConvertOne(jobs[i], o)
			}
		}()
	}
	for i := range jobs {
		ch <- i
	}
	close(ch)
	wg.Wait()
	return reps
}

// WriteFileAtomic writes data to a temp file in the same directory, syncs
// and atomically renames it over the target.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func formatForPath(p string) format.Format {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".srt":
		return format.SRT
	case ".vtt":
		return format.VTT
	}
	return ""
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil && absA == absB {
		return true
	}
	if inA, err := os.Stat(a); err == nil {
		if inB, err := os.Stat(b); err == nil {
			return os.SameFile(inA, inB)
		}
	}
	return false
}
