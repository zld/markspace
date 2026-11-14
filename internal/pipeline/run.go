// Package pipeline wires config, queue, media, quality and report together:
// probe inputs, segment, transcode, measure, persist.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"transcode/internal/config"
	"transcode/internal/media"
	"transcode/internal/quality"
	"transcode/internal/queue"
	"transcode/internal/report"
)

// JobSpec identifies one input to process.
type JobSpec struct {
	ID    string
	Input string
}

// Options parameterizes a run.
type Options struct {
	Cfg    config.Config
	Jobs   []JobSpec
	Resume bool
	Stdout io.Writer
}

type jobData struct {
	spec JobSpec
	info media.ProbeInfo
	ts   []float64
}

// Run executes the full pipeline and returns the aggregate report.
func Run(ctx context.Context, opts Options) (*report.Report, error) {
	cfg := opts.Cfg
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(opts.Jobs) == 0 {
		return nil, fmt.Errorf("pipeline: no input jobs")
	}
	if err := os.MkdirAll(filepath.Join(cfg.OutDir, ".tmp"), 0o755); err != nil {
		return nil, fmt.Errorf("pipeline: create output dir: %w", err)
	}

	runner := media.Runner{Timeout: cfg.Timeout}
	version, err := runner.Version(ctx)
	if err != nil {
		return nil, fmt.Errorf("pipeline: ffmpeg unavailable: %w", err)
	}

	// Probe every input up front: validates decodability and timestamps
	// before any job starts, and feeds the config digest.
	datas := make([]jobData, 0, len(opts.Jobs))
	for i, js := range opts.Jobs {
		if js.ID == "" {
			js.ID = fmt.Sprintf("job%02d", i)
		}
		info, err := runner.Probe(ctx, js.Input)
		if err != nil {
			return nil, err
		}
		ts, err := runner.Timestamps(ctx, js.Input)
		if err != nil {
			return nil, err
		}
		info.Frames = len(ts)
		datas = append(datas, jobData{spec: js, info: info, ts: ts})
	}

	digest, err := computeDigest(cfg, version, datas)
	if err != nil {
		return nil, err
	}

	statePath := filepath.Join(cfg.OutDir, "queue-state.jsonl")
	skip := map[string]bool{}
	if _, err := os.Stat(statePath); err == nil {
		if !opts.Resume {
			return nil, fmt.Errorf("pipeline: state log %s already exists; use --resume to continue or choose a fresh --out", statePath)
		}
		prevDigest, final, err := queue.ReadLog(statePath)
		if err != nil {
			return nil, err
		}
		if prevDigest != digest {
			return nil, fmt.Errorf("pipeline: config digest mismatch (previous %s, current %s); refusing to mix results across configurations", prevDigest, digest)
		}
		for id, st := range final {
			if st == queue.Success {
				skip[id] = true
			}
		}
	}
	log, err := queue.NewStateLog(statePath)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	if !opts.Resume {
		if err := log.WriteHeader(digest); err != nil {
			return nil, err
		}
	}

	dataByID := map[string]jobData{}
	var jobs []*queue.Job
	for _, d := range datas {
		dataByID[d.spec.ID] = d
		if skip[d.spec.ID] {
			continue
		}
		jobs = append(jobs, &queue.Job{ID: d.spec.ID, Input: d.spec.Input, State: queue.Pending})
	}

	var mu sync.Mutex
	jobReports := map[string]*report.JobReport{}
	pool := queue.Pool{Concurrency: cfg.Concurrency, Retries: cfg.Retries, Log: log}
	pool.Run(ctx, jobs, func(ctx context.Context, j *queue.Job) error {
		d := dataByID[j.ID]
		jr, err := processJob(ctx, cfg, runner, j.ID, d)
		if err != nil {
			return err
		}
		mu.Lock()
		jobReports[j.ID] = jr
		mu.Unlock()
		return nil
	})

	// Assemble the aggregate report in input order.
	rep := &report.Report{
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		FFmpegVersion:    version,
		Digest:           digest,
		Policy:           fmt.Sprintf("encoder=libx264 preset=%s pix_fmt=%s fps=%d(cfr) container=mp4", cfg.Preset, cfg.PixFmt, cfg.FPS),
		StatsPrecision:   report.StatsPrecision,
		PSNRSaturationDB: quality.SaturationDB,
		Config: map[string]string{
			"segment":      cfg.Segment.String(),
			"target_kbps":  fmt.Sprint(cfg.TargetKbps),
			"maxrate_kbps": fmt.Sprint(cfg.MaxrateKbps),
			"concurrency":  fmt.Sprint(cfg.Concurrency),
			"retries":      fmt.Sprint(cfg.Retries),
			"timeout":      cfg.Timeout.String(),
		},
		TargetKbps: cfg.TargetKbps,
	}
	var totalBytes int64
	var totalDur float64
	for _, d := range datas {
		id := d.spec.ID
		var jr *report.JobReport
		if skip[id] {
			jr, err = report.ReadJobJSON(filepath.Join(cfg.OutDir, id))
			if err != nil {
				return nil, fmt.Errorf("pipeline: resume: read committed report for %s: %w", id, err)
			}
			rep.JobsSuccess++
		} else {
			jr = jobReports[id]
		}
		if jr == nil {
			// Failed or canceled job: find its final state.
			st, errText := queue.Failed, ""
			for _, j := range jobs {
				if j.ID == id {
					st, errText = j.State, j.Err
				}
			}
			jr = &report.JobReport{ID: id, Input: d.spec.Input, State: string(st), Error: errText, InputSummary: d.info}
		}
		switch jr.State {
		case string(queue.Success):
			if !skip[id] {
				rep.JobsSuccess++
			}
			for _, s := range jr.Segments {
				totalBytes += s.SizeBytes
				totalDur += s.DurationSec
			}
		case string(queue.Canceled):
			rep.JobsCanceled++
		default:
			rep.JobsFailed++
		}
		rep.Jobs = append(rep.Jobs, *jr)
	}
	rep.JobsTotal = len(rep.Jobs)
	if totalDur > 0 {
		rep.ActualKbps = round3(float64(totalBytes) * 8 / totalDur / 1000)
		rep.DeviationPct = round3((rep.ActualKbps - float64(cfg.TargetKbps)) / float64(cfg.TargetKbps) * 100)
	}

	if err := report.WriteJSON(filepath.Join(cfg.OutDir, "report.json"), rep); err != nil {
		return nil, err
	}
	if opts.Stdout != nil {
		report.PrintSummary(opts.Stdout, rep)
	}
	return rep, nil
}

// processJob transcodes and measures one input. Work happens in a private
// temp dir that is atomically renamed to the final output dir on success and
// removed on failure.
func processJob(ctx context.Context, cfg config.Config, runner media.Runner, id string, d jobData) (jr *report.JobReport, err error) {
	finalDir := filepath.Join(cfg.OutDir, id)
	if _, statErr := os.Stat(finalDir); statErr == nil {
		return nil, fmt.Errorf("output %s already exists; refusing to overwrite", finalDir)
	}
	tmpDir := filepath.Join(cfg.OutDir, ".tmp", id)
	_ = os.RemoveAll(tmpDir) // drop stale temp from a crashed run
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	// On any failure, clean temp artifacts; cleanup failures join the summary.
	fail := func(cause error) (*report.JobReport, error) {
		if cerr := os.RemoveAll(tmpDir); cerr != nil {
			return nil, fmt.Errorf("%w; additionally failed to clean temp dir %s: %v", cause, tmpDir, cerr)
		}
		return nil, cause
	}

	jr = &report.JobReport{
		ID:           id,
		Input:        d.spec.Input,
		State:        string(queue.Success),
		InputSummary: d.info,
	}
	segSec := cfg.Segment.Seconds()
	nSeg := int(math.Ceil(d.info.Duration / segSec))
	enc := media.EncodeParams{
		TargetKbps:  cfg.TargetKbps,
		MaxrateKbps: cfg.MaxrateKbps,
		FPS:         cfg.FPS,
		Preset:      cfg.Preset,
		PixFmt:      cfg.PixFmt,
	}

	var allFrames []float64
	var totalBytes int64
	var totalDur float64
	for i := 0; i < nSeg; i++ {
		start := float64(i) * segSec
		dur := math.Min(segSec, d.info.Duration-start)
		sr := report.SegmentReport{
			Index:          i,
			StartSec:       round3(start),
			DurationSec:    round3(dur),
			ExpectedFrames: countFrames(d.ts, start, dur, i == nSeg-1),
		}
		segPath := filepath.Join(tmpDir, fmt.Sprintf("seg_%02d.mp4", i))
		statsPath := filepath.Join(tmpDir, fmt.Sprintf("seg_%02d.psnr", i))

		args, terr := runner.TranscodeSegment(ctx, d.spec.Input, segPath, start, dur, enc)
		sr.FFmpegArgs = args
		if terr != nil {
			return fail(fmt.Errorf("segment %d transcode: %w", i, terr))
		}
		if _, perr := runner.PSNR(ctx, segPath, d.spec.Input, start, dur, statsPath); perr != nil {
			return fail(fmt.Errorf("segment %d psnr: %w", i, perr))
		}
		psnr, perr := parsePSNRFile(statsPath)
		if perr != nil {
			return fail(fmt.Errorf("segment %d psnr parse: %w", i, perr))
		}
		sr.Frames = len(psnr)
		if len(psnr) != sr.ExpectedFrames {
			// Frame mismatch: mark invalid, never fabricate numbers.
			sr.Valid = false
			sr.InvalidReason = fmt.Sprintf("frame count mismatch: got %d, expected %d", len(psnr), sr.ExpectedFrames)
		} else {
			sum := quality.Summarize(psnr)
			sr.Valid = true
			sr.PSNRMeanDB = round3(sum.MeanDB)
			sr.PSNRMinDB = round3(sum.MinDB)
			sr.InfFrames = sum.InfFrames
			allFrames = append(allFrames, psnr...)
		}
		fi, serr := os.Stat(segPath)
		if serr != nil {
			return fail(fmt.Errorf("segment %d stat: %w", i, serr))
		}
		sr.SizeBytes = fi.Size()
		sr.ActualKbps = round3(float64(fi.Size()) * 8 / dur / 1000)
		if formatSize, videoBytes, oerr := runner.ContainerOverhead(ctx, segPath); oerr == nil && formatSize > 0 {
			sr.ContainerOverheadPct = round3(float64(formatSize-videoBytes) / float64(formatSize) * 100)
		}
		jr.Segments = append(jr.Segments, sr)
		totalBytes += fi.Size()
		totalDur += dur
	}

	if len(allFrames) > 0 {
		sum := quality.Summarize(allFrames)
		jr.PSNRMeanDB = round3(sum.MeanDB)
		jr.PSNRMinDB = round3(sum.MinDB)
		jr.Frames = sum.Frames
	}
	if totalDur > 0 {
		jr.ActualKbps = round3(float64(totalBytes) * 8 / totalDur / 1000)
		jr.DeviationPct = round3((jr.ActualKbps - float64(cfg.TargetKbps)) / float64(cfg.TargetKbps) * 100)
	}
	if err := report.WriteJobJSON(tmpDir, jr); err != nil {
		return fail(fmt.Errorf("write job report: %w", err))
	}

	// Atomic commit: rename the temp dir into place, still refusing to
	// overwrite anything that appeared meanwhile.
	if _, statErr := os.Stat(finalDir); statErr == nil {
		return fail(fmt.Errorf("output %s appeared during run; refusing to overwrite", finalDir))
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return fail(fmt.Errorf("commit output: %w", err))
	}
	jr.OutputDir = finalDir
	// Persist the final output path into the committed report.
	if err := report.WriteJobJSON(finalDir, jr); err != nil {
		return nil, fmt.Errorf("finalize job report: %w", err)
	}
	return jr, nil
}

func parsePSNRFile(path string) ([]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return quality.ParsePSNRStats(f)
}

// countFrames counts source frames with PTS in [start, start+dur). The last
// segment absorbs all remaining frames.
func countFrames(ts []float64, start, dur float64, last bool) int {
	end := start + dur
	n := 0
	for _, t := range ts {
		if t < start-1e-9 {
			continue
		}
		if !last && t >= end-1e-9 {
			continue
		}
		n++
	}
	return n
}

// computeDigest hashes everything that must match for a resumed run to be
// comparable: encoding parameters, FFmpeg build and input summaries.
func computeDigest(cfg config.Config, version string, datas []jobData) (string, error) {
	type inputSummary struct {
		Path     string  `json:"path"`
		Duration float64 `json:"duration"`
		Frames   int     `json:"frames"`
		Width    int     `json:"width"`
		Height   int     `json:"height"`
		Codec    string  `json:"codec"`
		Size     int64   `json:"size"`
	}
	payload := struct {
		Segment     string         `json:"segment"`
		TargetKbps  int            `json:"target_kbps"`
		MaxrateKbps int            `json:"maxrate_kbps"`
		FPS         int            `json:"fps"`
		PixFmt      string         `json:"pix_fmt"`
		Preset      string         `json:"preset"`
		FFmpeg      string         `json:"ffmpeg"`
		Inputs      []inputSummary `json:"inputs"`
	}{
		Segment:     cfg.Segment.String(),
		TargetKbps:  cfg.TargetKbps,
		MaxrateKbps: cfg.MaxrateKbps,
		FPS:         cfg.FPS,
		PixFmt:      cfg.PixFmt,
		Preset:      cfg.Preset,
		FFmpeg:      version,
	}
	for _, d := range datas {
		abs, err := filepath.Abs(d.spec.Input)
		if err != nil {
			return "", err
		}
		payload.Inputs = append(payload.Inputs, inputSummary{
			Path:     abs,
			Duration: d.info.Duration,
			Frames:   d.info.Frames,
			Width:    d.info.Width,
			Height:   d.info.Height,
			Codec:    d.info.CodecName,
			Size:     d.info.SizeBytes,
		})
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16], nil
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
