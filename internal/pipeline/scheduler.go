package pipeline

import (
	"path/filepath"

	"mediakernel/internal/cache"
	"mediakernel/internal/clock"
	"mediakernel/internal/fsx"
	"mediakernel/internal/index"
	"mediakernel/internal/meta"
	"mediakernel/internal/rtp"
	"mediakernel/internal/wav"
)

func fsxEnsure(base, rel string) (string, error) {
	return fsx.EnsureOutput(base, rel)
}

// Run executes the staged pipeline with cache and checkpoint support. When
// resume is true and a matching checkpoint exists, completed stages are
// skipped; otherwise every stage runs and its digest is recorded.
func Run(o Options, resume bool) (*RunResult, error) {
	if err := validateOptions(o); err != nil {
		return nil, err
	}
	start := nowMillis()
	wavPath, err := prepareWAV(o)
	if err != nil {
		return nil, err
	}
	inputDigest, err := digestFile(wavPath)
	if err != nil {
		return nil, err
	}

	params := cache.Params{
		Channels:      o.Channels,
		SampleRate:    o.SampleRate,
		BitsPerSample: 16,
		BlockFrames:   o.BlockFrames,
		ClockRate:     o.SampleRate,
		Layout:        layoutName(o),
		StageVersion:  StageIndexVersion,
	}
	paramsDigest := ParamsDigest(params)
	cpStore, err := NewCheckpointStore(o.BaseDir,
		filepath.Join(o.OutputDir, "checkpoint.json"))
	if err != nil {
		return nil, err
	}
	cp, err := cpStore.Load()
	if err != nil {
		if ce, ok := clock.AsError(err); ok && ce.Category == clock.CatCheckpoint {
			if rmErr := cpStore.Remove(); rmErr != nil {
				return nil, rmErr
			}
			cp = nil
		} else {
			return nil, err
		}
	}
	cacheStore, err := cache.NewStore(o.BaseDir, filepath.Join(o.OutputDir, "cache"))
	if err != nil {
		return nil, err
	}
	cacheKey := cache.Key(inputDigest, params)
	result := &RunResult{Frames: int64(o.Frames), InputDigest: inputDigest, FirstByteOffset: -1}

	// Stage 1: parse WAV. Truncation surfaces here with a byte offset.
	reader, err := wav.Open(o.BaseDir, relOf(o.BaseDir, wavPath))
	if err != nil {
		return nil, recordLocating(result, err)
	}
	defer reader.Close()
	header := reader.Header()
	parseDigest := digestHeader(header)

	// Stage 2: layout/metadata consistency plus the RTP clock-rate check.
	layout, err := meta.StandardLayouts(o.Channels, o.SampleRate)
	if err != nil {
		return nil, err
	}
	if err := meta.CheckWAV(layout, header); err != nil {
		return nil, recordLocating(result, err)
	}
	tl := clock.NewTimeline(o.SampleRate)
	extractor, err := rtp.NewExtractor(tl, 1, o.SampleRate)
	if err != nil {
		return nil, err
	}
	if err := meta.CheckRTPRate(layout, 1, o.SampleRate); err != nil {
		return nil, err
	}
	packets, err := rtp.GeneratePCMPackets(1, o.SampleRate, 4, 160)
	if err != nil {
		return nil, err
	}
	if _, err := extractor.ParseAll(packets); err != nil {
		return nil, err
	}
	validateDigest := digestValidation(o.Channels, o.SampleRate, layout.Name)

	// Stage 3: sparse index, restored from cache when the checkpoint and the
	// triple-digest cache entry both match.
	var idx *index.Index
	stages := []StageState{
		{Name: "parse", Version: StageParseVersion, InputDigest: inputDigest,
			ParamsDigest: paramsDigest, ArtifactDigest: parseDigest},
		{Name: "validate", Version: StageValidateVersion, InputDigest: inputDigest,
			ParamsDigest: paramsDigest, ArtifactDigest: validateDigest},
	}
	resumeFrom := 0
	if resume && cp != nil && cp.InputDigest == inputDigest && cp.ParamsDigest == paramsDigest {
		resumeFrom = cp.ReusableStages(expectedStages(inputDigest, paramsDigest, parseDigest, validateDigest))
	}
	if resumeFrom >= 3 {
		wantIndex := cp.Stages[2].ArtifactDigest
		blob, _, getErr := cacheStore.Get(cacheKey, wavPath, inputDigest, wantIndex)
		if getErr == nil {
			if restored, uErr := index.Unmarshal(blob); uErr == nil && restored.Digest() == wantIndex {
				idx = restored
			}
		}
	}
	if idx == nil {
		idx, err = index.Build(reader, tl, o.BlockFrames)
		if err != nil {
			return nil, recordLocating(result, err)
		}
		if err := cacheStore.Put(cache.Entry{
			SourceID:    wavPath,
			InputDigest: inputDigest,
			IndexDigest: idx.Digest(),
			Key:         cacheKey,
		}, idx.Marshal()); err != nil {
			return nil, err
		}
	}
	result.IndexEntries = len(idx.Entries)
	result.IndexDigest = idx.Digest()
	stages = append(stages, StageState{Name: "index", Version: StageIndexVersion,
		InputDigest: inputDigest, ParamsDigest: paramsDigest, ArtifactDigest: idx.Digest()})

	// Checkpoint fault: on the initial (non-resume) run, persist through the
	// index stage then stop. A resume run continues to completion and yields
	// the identical output digest; if its checkpoint was corrupt or params
	// changed, rebuilds from scratch without re-triggering the rehearsal.
	if !resume && o.Fault == FaultCheckpoint && o.CrashAfterStage == "index" {
		fresh := &Checkpoint{InputDigest: inputDigest, ParamsDigest: paramsDigest, Stages: stages}
		if err := cpStore.Save(fresh); err != nil {
			return nil, err
		}
		return nil, ErrCrashRehearsal
	}

	// Stage 4: gap-free interval verification over the whole stream.
	frameStarts, intervalCount, err := verifyRanges(reader, tl, idx)
	if err != nil {
		return nil, recordLocating(result, err)
	}
	queryDigest := QueryDigest(frameStarts, intervalCount)
	stages = append(stages, StageState{Name: "query", Version: StageQueryVersion,
		InputDigest: inputDigest, ParamsDigest: paramsDigest, ArtifactDigest: queryDigest})

	// Stage 5: report digest.
	outputSHA := ReportDigest(params, idx.Digest(), queryDigest, idx.TotalFrames)
	stages = append(stages, StageState{Name: "report", Version: StageReportVersion,
		InputDigest: inputDigest, ParamsDigest: paramsDigest, ArtifactDigest: outputSHA})

	if err := cpStore.Save(&Checkpoint{
		InputDigest: inputDigest, ParamsDigest: paramsDigest, Stages: stages,
	}); err != nil {
		return nil, err
	}
	result.ReusedStages = resumeFrom
	result.Resume = resume && resumeFrom > 0
	result.ResumeMillis = nowMillis() - start
	result.OutputSHA256 = outputSHA
	return result, nil
}

func validateOptions(o Options) error {
	if o.BaseDir == "" || o.WAVRelPath == "" || o.OutputDir == "" {
		return clock.NewError("pipeline", clock.CatInvalidParam,
			"base directory, WAV path and output directory are required")
	}
	if o.Channels == 0 || o.SampleRate == 0 || o.Frames <= 0 || o.BlockFrames <= 0 {
		return clock.NewError("pipeline", clock.CatInvalidParam,
			"channels/rate/frames/blockFrames must all be positive")
	}
	switch o.Fault {
	case FaultNone, FaultTruncate, FaultCheckpoint:
	default:
		return clock.NewError("pipeline", clock.CatInvalidParam,
			"unknown fault %q", string(o.Fault))
	}
	return nil
}

// expectedStages builds the matching template; stage artifacts for parse and
// validate are supplied because they are cheap recomputable digests, while
// index/query/report artifacts are compared only when present in both.
func expectedStages(inputDigest, paramsDigest, parseDigest, validateDigest string) []StageState {
	return []StageState{
		{Name: "parse", Version: StageParseVersion, InputDigest: inputDigest,
			ParamsDigest: paramsDigest, ArtifactDigest: parseDigest},
		{Name: "validate", Version: StageValidateVersion, InputDigest: inputDigest,
			ParamsDigest: paramsDigest, ArtifactDigest: validateDigest},
		{Name: "index", Version: StageIndexVersion, InputDigest: inputDigest,
			ParamsDigest: paramsDigest},
		{Name: "query", Version: StageQueryVersion, InputDigest: inputDigest,
			ParamsDigest: paramsDigest},
		{Name: "report", Version: StageReportVersion, InputDigest: inputDigest,
			ParamsDigest: paramsDigest},
	}
}

func layoutName(o Options) string {
	l, err := meta.StandardLayouts(o.Channels, o.SampleRate)
	if err != nil {
		return "unknown"
	}
	return l.Name
}

func relOf(base, abs string) string {
	rel, err := filepath.Rel(base, abs)
	if err != nil {
		return abs
	}
	return rel
}

func recordLocating(r *RunResult, err error) error {
	if ce, ok := clock.AsError(err); ok {
		r.LocatingErrors++
		if ce.HasOffset && r.FirstByteOffset < 0 {
			r.FirstByteOffset = ce.ByteOffset
		}
	}
	return err
}

func nowMillis() int64 {
	return timeNowUnixNano() / 1_000_000
}
