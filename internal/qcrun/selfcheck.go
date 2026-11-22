package qcrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"mediaqc/internal/remux"
	"mediaqc/internal/samples"
)

// Check is one selfcheck assertion result.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Report is the aggregate selfcheck result.
type Report struct {
	OK     bool    `json:"ok"`
	Passed int     `json:"passed"`
	Total  int     `json:"total"`
	Checks []Check `json:"checks"`
}

// Selfcheck runs the acceptance suite over freshly generated samples:
// the normal path with quantitative thresholds, corrupt paths with
// positioned faults, repair paths, index binding, mapping validation,
// recoverable interrupt + resume, and repeat-run consistency.
func Selfcheck(ctx context.Context, dir string) Report {
	var checks []Check
	record := func(name string, ok bool, detail string) {
		checks = append(checks, Check{Name: name, OK: ok, Detail: detail})
	}

	if err := samples.WriteAll(dir); err != nil {
		record("generate", false, err.Error())
		return report(checks)
	}
	record("generate", true, fmt.Sprintf("%d samples", len(samples.Names())))

	outDir := filepath.Join(dir, "out")
	os.MkdirAll(outDir, 0o755)
	run := func(input, output string, mutate func(*Options)) (remux.Summary, int) {
		opts := Options{
			Input:     filepath.Join(dir, input),
			Output:    filepath.Join(outDir, output),
			QueueCap:  4,
			MaxSkewNs: 40_000_000,
		}
		if mutate != nil {
			mutate(&opts)
		}
		return Run(ctx, opts)
	}

	// 1. Normal path with quantitative thresholds.
	normal, code := run("normal.webm", "normal.mp4", nil)
	ok := code == 0 &&
		normal.SyncErrorMs <= 2.0 &&
		normal.PacketLossRecoveryRate >= 0.90 &&
		normal.P95LatencyMs <= 250 &&
		normal.PeakBufferBytes <= 8<<20 &&
		normal.IndexEntries >= 1 &&
		normal.InputSHA256 != "" && normal.OutputSHA256 != ""
	record("normal-path", ok, fmt.Sprintf("exit=%d sync=%.2fms recovery=%.2f p95=%.1fms peak=%dB",
		code, normal.SyncErrorMs, normal.PacketLossRecoveryRate, normal.P95LatencyMs, normal.PeakBufferBytes))

	// 2. Corrupt paths: positioned truncation faults, exit 2.
	for _, name := range []string{"corrupt-trunc-unknown.webm", "corrupt-trunc-cluster.webm"} {
		sum, code := run(name, name+".mp4", nil)
		record("corrupt:"+name, code == 2 && sum.ErrorClass == "truncated" && sum.Offset > 0,
			fmt.Sprintf("exit=%d class=%s offset=%d", code, sum.ErrorClass, sum.Offset))
	}

	// 3. Repair paths: wrap and disorder are repaired, exit 0.
	wrap, code := run("corrupt-wrap.webm", "wrap.mp4", nil)
	record("repair:wrap", code == 0 && wrap.Repairs.Wraps >= 1,
		fmt.Sprintf("exit=%d wraps=%d", code, wrap.Repairs.Wraps))
	disorder, code := run("corrupt-disorder.webm", "disorder.mp4", nil)
	record("repair:disorder", code == 0 && disorder.Repairs.Regressions >= 1,
		fmt.Sprintf("exit=%d regressions=%d", code, disorder.Repairs.Regressions))

	// 4. Index binding: stale sidecar digest forces a rescan.
	mismatch, code := run("corrupt-mismatch.webm", "mismatch.mp4", nil)
	record("index:mismatch-rescan", code == 0 && mismatch.IndexAction == "rescanned",
		fmt.Sprintf("exit=%d action=%s", code, mismatch.IndexAction))

	// 5. Mapping validation: missing track fails before output exists.
	badMap, code := run("normal.webm", "badmap.mp4", func(o *Options) {
		o.Mapping = map[uint64]uint64{1: 1} // track 2 unmapped
	})
	_, statErr := os.Stat(filepath.Join(outDir, "badmap.mp4"))
	record("mapping:missing-track", code == 2 && badMap.ErrorClass == "track_mapping" && os.IsNotExist(statErr),
		fmt.Sprintf("exit=%d class=%s", code, badMap.ErrorClass))

	// 6. Recoverable interrupt: cancel mid-stream (exit 3), then resume.
	interrupted, code := run("normal.webm", "resumable.mp4", func(o *Options) {
		o.CancelAfter = 3
		o.Resume = true
	})
	record("interrupt:exit3", code == 3 && interrupted.ErrorClass == "interrupted",
		fmt.Sprintf("exit=%d class=%s", code, interrupted.ErrorClass))
	resumed, code := run("normal.webm", "resumable.mp4", func(o *Options) {
		o.Resume = true
	})
	record("resume:resumed", code == 0 && resumed.ResumeAction == "resumed",
		fmt.Sprintf("exit=%d action=%s", code, resumed.ResumeAction))

	// 7. Repeat-run consistency: identical index checksum, repair
	// counts and output digest across two runs of the same input.
	r1, c1 := run("normal.webm", "repeat1.mp4", nil)
	r2, c2 := run("normal.webm", "repeat2.mp4", nil)
	record("consistency:repeat", c1 == 0 && c2 == 0 &&
		r1.IndexChecksum == r2.IndexChecksum &&
		r1.Repairs == r2.Repairs &&
		r1.OutputSHA256 == r2.OutputSHA256,
		fmt.Sprintf("index=%t repairs=%t output=%t",
			r1.IndexChecksum == r2.IndexChecksum, r1.Repairs == r2.Repairs,
			r1.OutputSHA256 == r2.OutputSHA256))

	return report(checks)
}

func report(checks []Check) Report {
	r := Report{Checks: checks, Total: len(checks)}
	for _, c := range checks {
		if c.OK {
			r.Passed++
		}
	}
	r.OK = r.Passed == r.Total
	return r
}
