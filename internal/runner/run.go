package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"archive-lab/internal/manifest/scan"
	"archive-lab/internal/media"
	"archive-lab/internal/quality"
)

// maxSampleFrames bounds how many frames of the first segment are measured.
const maxSampleFrames = 12

// Run executes one restoration: prepare (parse, index, cache key), plan, then
// stream every adopted segment. Failures carry the completed segment count so
// the next run can resume from the first segment without an outcome.
func (r *Runner) Run(opts Options, cancel CancelFunc) *Result {
	if r.Now == nil {
		r.Now = time.Now
	}
	res := &Result{Report: &Report{Status: "error"}}
	rep := res.Report
	rep.TargetBps = opts.Request.TargetBps
	rep.MaxBps = opts.Request.MaxBps
	rep.MinPSNR = opts.Request.MinPSNR
	rep.BudgetMS = opts.Request.BudgetMs
	rep.Input = filepath.ToSlash(opts.InputPath)

	prep, err := r.Prepare(opts, 0)
	if err != nil {
		return res.fail(err, rep)
	}
	res.IndexRebuilt = prep.Rebuilt
	res.IndexDigest = prep.Index.Digest
	rep.IndexSHA256 = prep.Index.Digest
	rep.IndexRebuilt = prep.Rebuilt
	rep.InputSHA256 = prep.Index.Manifest.SHA256
	rep.InputDigest = prep.Index.InputDigest()
	rep.SegmentsTotal = 0

	segs, err := segmentRefsFor(prep.Playlist)
	if err != nil {
		return res.fail(err, rep)
	}
	rep.SegmentsTotal = len(segs)
	root := rootOf(opts)

	// Measure the first segment to ground the configuration decision. A
	// truncated or corrupt container fails here, before any index is relied
	// on for output.
	var sample quality.Sample
	var firstProbe *media.FileProbe
	if len(segs) > 0 {
		rel, rerr := scan.Resolve(root, segs[0].URI)
		if rerr != nil {
			return res.fail(rerr, rep)
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		sample, firstProbe, err = SegmentSample(full, segs[0].URI, int64(maxSampleFrames)*64*64*4)
		if err != nil {
			return res.fail(err, rep)
		}
	}

	segmentNs := int64(0)
	if len(segs) > 0 {
		segmentNs = segs[0].DurationNs
	}
	// Probe any RTP packet stream carried by the sample container. This
	// exercises timestamp/sequence validation on real bytes; a non-RTP
	// segment simply reports zero packets.
	if firstProbe != nil && len(firstProbe.PrivateRTP) > 0 {
		rtpProbe, rerr := ProbeRTP(firstProbe.PrivateRTP)
		if rerr != nil {
			return res.fail(rerr, rep)
		}
		rep.RTPPackets = rtpProbe.Packets
		rep.RTPDisorder = rtpProbe.Disordered
	}

	plan, err := quality.EstimatePlan(opts.Request, sample, len(segs), segmentNs)
	if err != nil {
		return res.fail(err, rep)
	}
	rep.ConfigSHA256 = plan.Config.Digest()
	rep.Codec = plan.Config.Codec
	rep.QuantizerMilli = plan.Config.QuantizerMilli
	rep.SegmentsAdopted = plan.Segments
	rep.SegmentsDeferred = plan.Deferred
	rep.Unmet = plan.Unmet
	rep.ConstraintsOK = plan.Feasible
	rep.Notes = append(rep.Notes, plan.Notes...)
	if !plan.Feasible {
		rep.Notes = append(rep.Notes, plan.Reason)
	}

	// Cache lookup. The key covers input and configuration; a stale input
	// entry is purged so a replaced file cannot be served from cache.
	cache := r.Cache
	segDigests := make(map[string]string)
	for _, f := range prep.Index.Files {
		segDigests[f.Path] = f.SHA256
	}
	key := Key(rep.InputDigest, rep.ConfigSHA256)
	entry, state, cerr := cache.Load(key, segDigests)
	if cerr != nil {
		return res.fail(cerr, rep)
	}
	res.CacheState = state
	rep.Cache = string(state)
	// An input whose content changed at the same path makes the previous
	// input's entries stale. Purge exactly those; entries for the current
	// input and for other inputs are left alone.
	if cache != nil && prep.ReplacedDigest != "" && prep.ReplacedDigest != rep.InputDigest {
		if _, perr := cache.PurgeForInput(prep.ReplacedDigest); perr != nil {
			rep.Notes = append(rep.Notes, "cache purge failed: "+perr.Error())
		}
	}

	if opts.DryRun {
		rep.Status = "dry_run"
		rep.ConstraintsOK = plan.Feasible
		rep.Notes = append(rep.Notes, "dry run: no segment payload was streamed")
		return res
	}

	// Resume state is only honoured when it belongs to exactly this input
	// digest and configuration. Anything else restarts at the first pending
	// segment.
	resumedFrom := 0
	adopted := plan.Segments
	if opts.ProgressPath != "" {
		prog, perr := ReadProgress(opts.ProgressPath)
		if perr != nil {
			return res.fail(perr, rep)
		}
		if prog != nil && prog.InputDigest == rep.InputDigest && prog.ConfigHash == rep.ConfigSHA256 && resumablePrefix(prog.Done, adopted) {
			resumedFrom = len(prog.Done)
			adopted = adopted[resumedFrom:]
		}
	}
	rep.ResumedFrom = resumedFrom

	// Stream every adopted segment, verifying bytes and digests.
	outcomes, inBytes, serr := r.streamSegments(opts, prep, segs, adopted, plan.Config, cancel)
	rep.SegmentsDone = len(outcomes)
	if resumedFrom > 0 {
		rep.SegmentsDone += resumedFrom
	}
	rep.BytesIn = inBytes
	res.Outcomes = outcomes
	if serr != nil {
		stage, _ := media.StageOf(serr)
		rep.FailedStage = string(stage)
		res.fail(serr, rep)
		res.Report.SegmentsDone = len(outcomes) + resumedFrom
		// Persist partial progress so a rerun resumes at the first segment
		// without an outcome. The key is unchanged; the stored entry
		// records how far it got.
		if cache != nil && len(outcomes) > 0 {
			_ = cache.Store(Entry{
				Key: key, InputDigest: rep.InputDigest, ConfigHash: rep.ConfigSHA256,
				SegmentDigests: segDigests, SegmentsDone: rep.SegmentsDone,
				SegmentsTotal: len(segs), Bytes: inBytes,
				ReportDigest: rep.Digest(),
			})
		}
		if opts.ProgressPath != "" {
			_ = WriteProgress(opts.ProgressPath, ProgressFile{
				InputDigest: rep.InputDigest, ConfigHash: rep.ConfigSHA256,
				Done:         doneIndices(resumedFrom, outcomes),
				ReportDigest: rep.Digest(),
			})
		}
		return res
	}

	// Measure output on the measured sample: real encoded bytes and real SSE.
	outBytes, outPSNR, psnrKnown := measureOutput(sample, plan.Config)
	rep.BytesOut = outBytes
	if sample.Measurable() && firstProbe != nil {
		if firstProbe.DurationNanos() > 0 {
			drift := quality.DriftMS(firstProbe.DurationNanos(), segs[0].DurationNs)
			rep.PlaylistDriftMS = drift
			if drift > 1.0 {
				rep.ConstraintsOK = false
				rep.Unmet = append(rep.Unmet, fmt.Sprintf("playlist_drift_ms: %.3f exceeds 1 ms", drift))
			}
		}
		// Actual bitrate comes from encoded bytes and the source's exact
		// frame clock.
		if sample.Spec.FPSNum > 0 && len(sample.Frames) > 0 {
			rep.ActualBps = int64(outBytes) * 8 * sample.Spec.FPSNum / (int64(len(sample.Frames)) * sample.Spec.FPSDen)
		}
	}
	if rep.ActualBps > 0 {
		rep.BitrateErrorPct = quality.BitrateErrorPct(rep.ActualBps, opts.Request.TargetBps)
		if absFloat(rep.BitrateErrorPct) > 5 {
			rep.ConstraintsOK = false
			rep.Unmet = append(rep.Unmet, fmt.Sprintf("bitrate_error_pct: %.2f exceeds 5%%", rep.BitrateErrorPct))
			rep.Status = "constraint_violation"
		}
		if rep.ActualBps > opts.Request.MaxBps {
			rep.ConstraintsOK = false
			rep.Unmet = append(rep.Unmet, fmt.Sprintf("max_bps: actual %d exceeds cap %d", rep.ActualBps, opts.Request.MaxBps))
			rep.Status = "constraint_violation"
		}
	}
	setPSNR(rep, outPSNR, psnrKnown)
	if psnrKnown && outPSNR < opts.Request.MinPSNR {
		rep.ConstraintsOK = false
		rep.Unmet = append(rep.Unmet, fmt.Sprintf("min_psnr: %.2f dB below %.2f dB", outPSNR, opts.Request.MinPSNR))
		rep.Status = "constraint_violation"
	}
	if rep.Status != "constraint_violation" {
		if rep.ConstraintsOK {
			rep.Status = "ok"
		} else {
			rep.Status = "feasible_with_deferrals"
		}
	}

	if opts.ProgressPath != "" {
		_ = WriteProgress(opts.ProgressPath, ProgressFile{
			InputDigest: rep.InputDigest, ConfigHash: rep.ConfigSHA256,
			Done:         doneIndices(resumedFrom, outcomes),
			ReportDigest: rep.Digest(),
		})
	}
	// Commit the completed entry only when the whole adopted prefix ran.
	if cache != nil && rep.Status != "error" {
		if err := cache.Store(Entry{
			Key: key, InputDigest: rep.InputDigest, ConfigHash: rep.ConfigSHA256,
			SegmentDigests: segDigests, SegmentsDone: rep.SegmentsDone,
			SegmentsTotal: len(segs), Bytes: inBytes,
			ReportDigest: rep.Digest(),
		}); err != nil {
			return res.fail(err, rep)
		}
	} else if cache != nil && state == CacheHit && entry != nil {
		// A hit may have a stored report digest; make sure our recomputation
		// agrees so a hit cannot mask non-determinism.
		if entry.ReportDigest != "" && entry.ReportDigest != rep.Digest() {
			return res.fail(media.New(media.StageCache, "cache", "cached report digest differs from recomputed report"), rep)
		}
	}
	return res
}

// measureOutput re-encodes the measured frames and reports payload bytes and
// PSNR. It never returns a PSNR for a source it could not measure.
func measureOutput(sample quality.Sample, cfg quality.Config) (int64, float64, bool) {
	if !sample.Measurable() {
		return 0, 0, false
	}
	choice, err := quality.ChooseGroup(sample.Frames, sample.Spec.Width, sample.Spec.Height,
		sample.Spec.FrameBytes(cfg.TargetBps)*int64(len(sample.Frames)), 0)
	if err != nil {
		return 0, 0, false
	}
	psnr, ok := quality.PSNRFromSSE(choice.SSE, int64(sample.Spec.Width*sample.Spec.Height*len(sample.Frames)))
	if !ok {
		return int64(choice.Bytes), 0, false
	}
	return int64(choice.Bytes), psnr, true
}

func (res *Result) fail(err error, rep *Report) *Result {
	res.Error = err
	rep.Status = "error"
	rep.Error = err.Error()
	if stage, ok := media.StageOf(err); ok {
		rep.FailedStage = string(stage)
	}
	return res
}

// ProgressFile records completed segment indices for resume. It is written
// atomically next to the artifact directory.
type ProgressFile struct {
	InputDigest  string `json:"input_digest"`
	ConfigHash   string `json:"config_digest"`
	Done         []int  `json:"done"`
	ReportDigest string `json:"report_digest"`
}

// WriteProgress persists resume state.
func WriteProgress(path string, p ProgressFile) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return media.Wrap(media.StageReport, "progress", err, "marshal progress")
	}
	return writeAtomic(path, data)
}

// ReadProgress loads resume state; a missing or corrupt file means "start at
// the first segment" rather than "skip ahead".
func ReadProgress(path string) (*ProgressFile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, media.Wrap(media.StageReport, "progress", err, "read progress").WithPath(path)
	}
	var p ProgressFile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, nil
	}
	return &p, nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return media.Wrap(media.StageReport, "write", err, "create directory").WithPath(path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".out-*.tmp")
	if err != nil {
		return media.Wrap(media.StageReport, "write", err, "create temp file").WithPath(path)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return media.Wrap(media.StageReport, "write", err, "write temp file").WithPath(path)
	}
	if err := tmp.Close(); err != nil {
		return media.Wrap(media.StageReport, "write", err, "close temp file").WithPath(path)
	}
	if err := os.Rename(name, path); err != nil {
		return media.Wrap(media.StageReport, "write", err, "commit file").WithPath(path)
	}
	return nil
}

// ConfigDigestOf is the digest of the resolved request without measurement.
func ConfigDigestOf(cfg quality.Config) string { return cfg.Digest() }

// ConfigFromOptions resolves a request into the default candidate config.
func ConfigFromOptions(req quality.Request) quality.Config {
	return quality.Config{Codec: quality.CodecVersion, TargetBps: req.TargetBps, MaxBps: req.MaxBps, MinPSNR: req.MinPSNR, BudgetMs: req.BudgetMs}
}

// HashFileHex is a small helper for tests and the CLI.
func HashFileHex(path string) (string, error) {
	d, err := scan.HashFile(path, 0)
	if err != nil {
		return "", err
	}
	return d.SHA256, nil
}

// resumablePrefix reports whether done is exactly the prefix 0..n-1 of the
// adopted list, i.e. a contiguous set of already-completed segments.
func resumablePrefix(done []int, adopted []int) bool {
	if len(done) == 0 {
		return false
	}
	if len(done) > len(adopted) {
		return false
	}
	for i, v := range done {
		if adopted[i] != v {
			return false
		}
	}
	return true
}

func doneIndices(resumedFrom int, outcomes []SegmentOutcome) []int {
	done := make([]int, 0, resumedFrom+len(outcomes))
	for i := 0; i < resumedFrom; i++ {
		done = append(done, i)
	}
	for _, o := range outcomes {
		done = append(done, o.Index)
	}
	return done
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
