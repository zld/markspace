// Package demo drives end-to-end scenarios: generate material, scan, process,
// build a manifest, verify it, and report the result for terminal output.
package demo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediacore/index"
	"mediacore/ingest"
	"mediacore/internal/fixture"
	"mediacore/manifest"
	"mediacore/media"
	"mediacore/pipeline"
)

// Case names accepted by the CLI.
const (
	CaseClean       = "clean"
	CaseBurstLoss   = "burst-loss"
	CaseCorrupt     = "corrupt"
	CaseTruncated   = "truncated"
	CaseWrap        = "wrap"
	CaseDigestShift = "digest-shift"
	CasePackets     = "packets"
)

// Cases lists every demo scenario in a stable order.
func Cases() []string {
	return []string{CaseClean, CaseBurstLoss, CaseCorrupt, CaseTruncated, CaseWrap, CaseDigestShift, CasePackets}
}

// Known reports whether name is a demo case.
func Known(name string) bool {
	for _, c := range Cases() {
		if c == name {
			return true
		}
	}
	return false
}

// Report is the JSON sidecar written next to each demo's outputs.
type Report struct {
	Case            string             `json:"case"`
	OutputDir       string             `json:"output_dir"`
	Frames          int                `json:"frames"`
	Segments        int                `json:"segments"`
	SilenceMS       int64              `json:"silence_ms"`
	DuplicateGroups int                `json:"duplicate_groups"`
	TargetKBPS      int                `json:"target_kbps"`
	ActualKBPS      int                `json:"actual_kbps"`
	DeviationPct    float64            `json:"deviation_pct"`
	AlignmentMS     int64              `json:"alignment_error_ms"`
	ManifestDigest  string             `json:"manifest_digest"`
	IndexDigest     string             `json:"index_digest"`
	InputDigest     string             `json:"input_digest"`
	MissingFrames   int                `json:"missing_frames"`
	GapCount        int                `json:"gap_count"`
	Duplicates      int                `json:"duplicate_packets"`
	Late            int                `json:"late_packets"`
	Checks          []manifest.Check   `json:"checks,omitempty"`
	Failures        []manifest.Failure `json:"failures,omitempty"`
	Notes           []string           `json:"notes,omitempty"`
	IndexCache      *index.CacheReport `json:"index_cache,omitempty"`
	Recovered       bool               `json:"recovered,omitempty"`
	DamageOffset    int64              `json:"damage_offset,omitempty"`
	ExitCode        int                `json:"exit_code"`
	// Expected documents the acceptance values this case asserts.
	Expected map[string]string `json:"expected,omitempty"`
}

// Options control one demo run.
type Options struct {
	Case    string
	OutDir  string
	Frames  int
	Verbose bool
	Print   func(string, ...any)
}

// Runner holds shared state for a run.
type Runner struct {
	opts  Options
	plan  fixture.Plan
	fopts fixture.Options
}

// Run executes the scenario, writing outputs into opts.OutDir. It returns the
// report, the process exit code, and any fatal error (exit code 2).
func Run(opts Options) (*Report, int, error) {
	if !Known(opts.Case) {
		return nil, 2, media.New(media.CatInvalidParams, media.StageCLI, "unknown demo case %q", opts.Case)
	}
	abs, err := filepath.Abs(opts.OutDir)
	if err != nil {
		return nil, 2, media.Wrap(media.CatIO, media.StageCLI, err, "resolve output directory")
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, 2, media.Wrap(media.CatIO, media.StageCLI, err, "create output directory")
	}
	plan := fixture.DefaultPlan()
	fopts := fixture.DefaultOptions()
	if opts.Frames > 0 {
		fopts.Frames = opts.Frames
		plan.Frames = opts.Frames
		// Scale the quiet window and duplicate pairs into the smaller sample
		// while keeping each derived feature large enough to be observable:
		// at least 25 quiet frames (1 s) and three pairs that fit.
		plan.QuietStart = fopts.Frames * 2 / 5
		plan.QuietCount = fopts.Frames / 4
		if plan.QuietCount < 25 {
			plan.QuietCount = 25
		}
		if plan.QuietStart+plan.QuietCount > fopts.Frames {
			plan.QuietStart = fopts.Frames - plan.QuietCount
			if plan.QuietStart < 0 {
				plan.QuietStart = 0
			}
		}
		// Place the duplicate targets in the back third, distinct from content.
		base := fopts.Frames - 10
		plan.DuplicatePairs = [][2]int{
			{1, base},
			{2, base + 1},
			{3, base + 2},
		}
		for i := range plan.DuplicatePairs {
			if plan.DuplicatePairs[i][1] >= fopts.Frames {
				plan.DuplicatePairs[i][1] = fopts.Frames - 1
			}
		}
	}
	r := &Runner{opts: opts, plan: plan, fopts: fopts}
	rep, code, err := r.run(abs)
	if rep != nil {
		rep.OutputDir = abs
		rep.Case = opts.Case
		rep.ExitCode = code
		b, _ := json.MarshalIndent(rep, "", "  ")
		_ = os.WriteFile(filepath.Join(abs, "report.json"), append(b, '\n'), 0o644)
	}
	return rep, code, err
}

func (r *Runner) run(dir string) (*Report, int, error) {
	rep := &Report{}
	switch r.opts.Case {
	case CaseClean:
		return r.clean(dir, rep)
	case CaseBurstLoss:
		return r.burstLoss(dir, rep)
	case CaseCorrupt:
		return r.corrupt(dir, rep)
	case CaseTruncated:
		return r.truncated(dir, rep)
	case CaseWrap:
		return r.wrap(dir, rep)
	case CaseDigestShift:
		return r.digestShift(dir, rep)
	case CasePackets:
		return r.packets(dir, rep)
	}
	return nil, 2, nil
}

// writeOutputs writes frames.bin (or its damaged form), index.json and
// manifest.json into dir.
func (r *Runner) writeOutputs(dir string, data []byte) error {
	return os.WriteFile(filepath.Join(dir, "frames.bin"), data, 0o644)
}

func (r *Runner) generate() ([]*media.Frame, []byte, error) {
	frames, err := r.plan.BuildFrames(r.fopts)
	if err != nil {
		return nil, nil, err
	}
	data, err := fixture.EncodeFrames(frames)
	if err != nil {
		return nil, nil, err
	}
	return frames, data, nil
}

// process scans data (cached in dir), runs the pipeline, and builds the
// manifest, returning everything needed to report and verify.
func (r *Runner) process(dir string, data []byte) (*index.Index, *pipeline.Result, *manifest.Manifest, index.CacheReport, error) {
	idx, rep := index.EnsureCache(cachePath(dir), digestOf(data), func() (*index.Index, error) {
		idx, err := index.Scan(bytes.NewReader(data), index.DefaultOptions())
		if err != nil {
			// Persist the valid prefix so a damaged index can be inspected,
			// then surface the scan failure.
			_ = writeIndexBestEffort(indexPath(dir), idx)
			return idx, err
		}
		return idx, nil
	})
	if idx != nil {
		// Always publish the plain index document when a scan produced one.
		if b, merr := idx.Marshal(); merr == nil {
			_ = writeFile(dir, "index.json", b)
		}
	}
	if idx == nil || !idx.Complete {
		return idx, nil, nil, rep, nil
	}
	res, err := pipeline.Run(idx, pipeline.NewBufferSource(data, media.DefaultLimits()), pipeline.DefaultConfig(media.DefaultTimescale))
	if err != nil {
		return idx, nil, nil, rep, err
	}
	timing, err := manifest.BindTiming(idx.Entries, res.MediaDurationMS)
	if err != nil {
		return idx, nil, nil, rep, err
	}
	bw := bandwidthFromPackets(data, r.fopts.FrameMS)
	// The target is whatever the estimator recommends under the input
	// ceiling; it is never a constant. For the default material the estimate
	// is 900 kbps, which is what the acceptance line documents.
	target := bw.TargetKBPS
	if target <= 0 {
		target = 900
	}
	m, err := manifest.Build(idx, res, manifest.Options{
		TargetKBPS: target,
		Bandwidth: &manifest.BandwidthReportLike{
			EstimateKBPS:     bw.EstimateKBPS,
			InputCeilingKBPS: bw.InputCeilingKBPS,
			TargetKBPS:       target,
			Reason:           bw.Reason,
		},
		Timing: timing,
	})
	if err != nil {
		return idx, nil, nil, rep, err
	}
	return idx, res, m, rep, nil
}

// bandwidthFromPackets estimates bandwidth from generated RTP packets. The
// packet timeline uses the frame cadence, which yields 900 kbps at the default
// 4500-byte payload.
func bandwidthFromPackets(frameData []byte, frameMS int64) ingest.BandwidthReport {
	frames := rFrameCount(frameData)
	// One packet per frame, delivered at the frame cadence.
	est := ingest.NewBandwidthEstimator(ingest.DefaultBandwidthConfig())
	bytesPerFrame := int64(fixture.PacketPayload)
	totalMS := int64(frames) * frameMS
	if totalMS <= 0 {
		return est.Report()
	}
	// Deliver in 100 ms buckets so the estimator sees smooth intervals.
	const bucketMS = 100
	perBucket := bucketMS * bytesPerFrame / frameMS
	for t := int64(bucketMS); t <= totalMS; t += bucketMS {
		est.Observe(t, perBucket)
	}
	return est.Report()
}

// rFrameCount counts records in a frame stream without validating content.
func rFrameCount(data []byte) int {
	if fixture.RecordSizeFor(fixture.DefaultOptions()) == 0 {
		return 0
	}
	return len(data) / fixture.RecordSizeFor(fixture.DefaultOptions())
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeIndexBestEffort stores a successfully scanned (possibly partial) index
// for inspection. It deliberately reuses the atomic cache writer so a crash
// mid-write cannot leave a truncated document at the canonical path.
func writeIndexBestEffort(path string, idx *index.Index) error {
	if idx == nil {
		return nil
	}
	return index.WriteCache(path, idx.InputDigest, idx)
}

// fail builds a failure report with the required terminal lines.
func (r *Runner) fail(rep *Report, err error) (*Report, int, error) {
	if e, ok := media.AsError(err); ok {
		rep.Failures = append(rep.Failures, manifest.Failure{
			Category: e.Category,
			Field:    string(e.Stage),
			Expected: "clean input",
			Actual:   e.Message,
			Detail:   fmt.Sprintf("offset=%d", e.FrameOffset),
		})
		if e.FrameOffset > 0 {
			rep.DamageOffset = e.FrameOffset
		}
	}
	return rep, 1, err
}
