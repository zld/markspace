package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/local/tracecheck/internal/cache"
	"github.com/local/tracecheck/internal/checkpoint"
	"github.com/local/tracecheck/internal/clock"
	"github.com/local/tracecheck/internal/fixture"
	"github.com/local/tracecheck/internal/mkv"
	"github.com/local/tracecheck/internal/pipeline"
)

// selfCase is one built-in corruption or recovery scenario.
type selfCase struct {
	name string
	run  func(workDir string) selfResult
}

// selfResult captures the locatable evidence and pass/fail conclusion.
type selfResult struct {
	pass     bool
	code     clock.Code
	offset   int64
	interval string
	detail   string
}

func (r selfResult) print() {
	status := "PASS"
	if !r.pass {
		status = "FAIL"
	}
	fmt.Printf("  status=%s code=%s first_offset=%d anomaly_range=%s detail=%s\n",
		status, r.code, r.offset, r.interval, r.detail)
}

// cmdSelfTest executes every built-in case and returns 0 only when all pass.
func cmdSelfTest(o options) int {
	base := o.resumeDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "tracecheck-selftest")
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create work dir: %v\n", err)
		return exitInput
	}
	cases := []selfCase{
		{"truncated_unknown_length_element", caseTruncatedUnknown},
		{"illegal_vint", caseIllegalVINT},
		{"interleaved_time_regression", caseTimeRegression},
		{"cache_source_mismatch", caseCacheMismatch},
		{"interrupt_resume", caseInterruptResume},
	}
	failed := 0
	for i, c := range cases {
		fmt.Printf("case %d: %s\n", i+1, c.name)
		work := filepath.Join(base, c.name)
		_ = os.RemoveAll(work)
		res := c.run(work)
		res.print()
		if !res.pass {
			failed++
		}
	}
	if failed > 0 {
		fmt.Printf("verdict=SELFTEST_FAILED (%d/%d cases)\n", failed, len(cases))
		return exitBusiness
	}
	fmt.Println("verdict=OK")
	return exitOK
}

func parseErr(data []byte) *clock.TraceError {
	_, err := mkv.Parse(bytes.NewReader(data), int64(len(data)))
	return clock.AsTraceError(err)
}

func caseTruncatedUnknown(workDir string) selfResult {
	data, wantOffset := fixture.TruncatedUnknownLength()
	te := parseErr(data)
	if te == nil {
		return selfResult{pass: false, detail: "expected a parse error"}
	}
	return selfResult{
		pass: te.Code == clock.CodeTruncatedElement && te.Offset == wantOffset,
		code: te.Code, offset: te.Offset, interval: te.Path,
		detail: te.Msg,
	}
}

func caseIllegalVINT(workDir string) selfResult {
	data, wantOffset := fixture.IllegalVINT()
	te := parseErr(data)
	if te == nil {
		return selfResult{pass: false, detail: "expected illegal VINT"}
	}
	return selfResult{
		pass: te.Code == clock.CodeIllegalVINT && te.Offset == wantOffset,
		code: te.Code, offset: te.Offset, interval: te.Path, detail: te.Msg,
	}
}

func caseTimeRegression(workDir string) selfResult {
	data, wantOffset := fixture.TimeRegression()
	doc, err := mkv.Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return selfResult{pass: false, detail: "syntax must remain valid: " + err.Error()}
	}
	if len(doc.Anomalies) == 0 {
		return selfResult{pass: false, detail: "expected a time anomaly"}
	}
	a := doc.Anomalies[0]
	pass := a.Code == clock.CodeTimeRegression && a.SourceOffset == wantOffset
	return selfResult{
		pass: pass, code: a.Code, offset: a.SourceOffset,
		interval: fmt.Sprintf("abs_ns=[%d,%d]", int64(a.AbsTime), int64(a.PrevTime)),
		detail:   fmt.Sprintf("track=%d raw=%d", a.TrackNumber, a.RawTimecode),
	}
}

func caseCacheMismatch(workDir string) selfResult {
	content := []byte("cached-segment-bytes")
	cas, err := cache.Open(filepath.Join(workDir, "cache"))
	if err != nil {
		return selfResult{pass: false, detail: err.Error()}
	}
	srcA := cache.Source{InputID: "input-A", Start: 0, End: int64(len(content))}
	if _, err := cas.Put(srcA, content); err != nil {
		return selfResult{pass: false, detail: err.Error()}
	}
	// Same bytes and range, different input identity: must not hit.
	srcB := cache.Source{InputID: "input-B", Start: 0, End: int64(len(content))}
	rec, mismatch := cas.Lookup(srcB, content)
	pass := rec == nil && mismatch != nil && mismatch.Reason == clock.CodeCacheMismatch &&
		cas.Hits == 0
	return selfResult{
		pass: pass, code: clock.CodeCacheMismatch, offset: 0,
		detail: mismatchDetail(mismatch),
	}
}

func caseInterruptResume(workDir string) selfResult {
	valid := fixture.Valid(fixture.DefaultOptions())
	doc, err := mkv.Parse(bytes.NewReader(valid), int64(len(valid)))
	if err != nil {
		return selfResult{pass: false, detail: err.Error()}
	}
	cacheDir := filepath.Join(workDir, "cache")
	resumeDir := filepath.Join(workDir, "resume")
	cfg := pipeline.Config{
		InputID: pipeline.InputDigest(valid), SegmentNS: 40_000_000,
		MaxSkewNS: clock.Tick(100_000_000), QueueCapacity: 4, MaxItemBytes: maxItemBytes,
		SlowConsumerMS: 25, CacheDir: cacheDir, ResumeDir: resumeDir,
		Layers: []pipeline.LayerSpec{
			{Bitrate: 16000, TrackNumber: doc.Tracks[0].Number},
			{Bitrate: 32000, TrackNumber: doc.Tracks[1].Number},
		},
	}
	// Interrupted run: a tight timeout cancels the slow consumer mid-way.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	_, ierr := pipeline.Run(ctx, doc, cfg)
	cancel()
	// The authoritative evidence of progress is the on-disk checkpoint,
	// since a cancelled run returns no manifest.
	committed := 0
	if store, serr := checkpoint.OpenStore(resumeDir); serr == nil {
		if st, lerr := store.Load(cfg.InputID); lerr == nil {
			committed = store.NextIndex(st)
		}
	}
	// Resume to completion.
	cfg.SlowConsumerMS = 0
	resumed, err := pipeline.Run(context.Background(), doc, cfg)
	if err != nil {
		return selfResult{pass: false, code: clock.CodeCancelled, detail: "resume: " + err.Error()}
	}
	// A clean run in separate directories is the reference.
	cleanCfg := cfg
	cleanCfg.CacheDir = filepath.Join(workDir, "clean-cache")
	cleanCfg.ResumeDir = filepath.Join(workDir, "clean-resume")
	clean, err := pipeline.Run(context.Background(), doc, cleanCfg)
	if err != nil {
		return selfResult{pass: false, detail: "clean: " + err.Error()}
	}
	pass := resumed.Manifest.ManifestSHA256 == clean.Manifest.ManifestSHA256 &&
		clock.AsTraceError(ierr) != nil && clock.AsTraceError(ierr).Code == clock.CodeCancelled
	return selfResult{
		pass: pass, code: clock.CodeCancelled,
		offset:   int64(committed),
		interval: fmt.Sprintf("resumed_from_segment=%d", committed),
		detail:   "manifest_sha256 matches clean run",
	}
}

func mismatchDetail(m *cache.Mismatch) string {
	if m == nil {
		return ""
	}
	return m.Detail
}
