package pipeline

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"mediarc/internal/buffer"
	"mediarc/internal/checkpoint"
	"mediarc/internal/ingest"
	"mediarc/internal/media"
	"mediarc/internal/model"
	"mediarc/internal/recovery"
	"mediarc/internal/report"
)

// RunResult contains the summary, checkpoint recovery facts and error.
type RunResult struct {
	Options  Options
	Summary  report.Summary
	Recovery checkpoint.Recovery
	Faults   *model.Collector
}

// LoadOptions reads persisted demo options and applies resume-only flags.
func LoadOptions(workdir string, interruptAfter int) (Options, error) {
	var o Options
	b, err := os.ReadFile(ConfigPath(workdir))
	if err != nil {
		return o, err
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return o, err
	}
	o.Workdir = workdir
	o.InterruptAfter = interruptAfter
	if o.ChunkSize == 0 {
		o.ChunkSize = 16
	}
	return o, nil
}

// Run executes resumable chunk processing. Returned errors preserve exit codes.
func Run(o Options) (*RunResult, error) {
	started := time.Now()
	if o.ChunkSize == 0 {
		o.ChunkSize = 16
	}
	if o.Window == 0 {
		o.Window = 64
	}
	if o.LateThreshold == 0 {
		o.LateThreshold = 8
	}
	p := NewPaths(o.Workdir)
	faults := &model.Collector{}
	sum := report.Summary{Mode: "resume", Workdir: o.Workdir, Seed: o.Seed, ChunkSize: o.ChunkSize}

	srcLayout, dstLayout, layoutErr := o.Layouts()
	if layoutErr != nil {
		sum.ExitCode = model.ExitSyncConfig
		return &RunResult{Summary: sum, Faults: faults}, layoutErr
	}

	inputSHA, err := CombinedInputHash(p)
	if err != nil {
		return &RunResult{Summary: sum, Faults: faults}, err
	}
	paramsSHA := o.ParamsHash()
	chunkCount := ChunkCount(o.Frames, o.ChunkSize)
	store := checkpoint.New(o.Workdir)
	idx, rec, err := store.LoadOrRecover(inputSHA, paramsSHA, chunkCount, o.ChunkSize)
	if err != nil {
		return &RunResult{Summary: sum, Recovery: rec, Faults: faults}, err
	}

	var manifest FixtureManifest
	if mb, err := os.ReadFile(p.Fixture); err == nil {
		_ = json.Unmarshal(mb, &manifest)
	}

	metas, err := ingest.ScanFrameFile(p.Frames, faults)
	if err != nil {
		return &RunResult{Summary: sum, Recovery: rec, Faults: faults}, err
	}
	storedPackets, err := ingest.ReadRTPFile(p.Audio, faults)
	if err != nil {
		return &RunResult{Summary: sum, Recovery: rec, Faults: faults}, err
	}
	jb := buffer.New(o.Window, o.LateThreshold, model.AudioClockRate, faults)
	for _, pkt := range storedPackets {
		jb.Push(pkt)
	}
	items, gaps := jb.Flush()
	jstats := jb.Stats()
	frames := recovery.RestoreFrames(metas, o.Frames, faults)
	validFrames := 0
	for _, m := range metas {
		if m.Valid {
			validFrames++
		}
	}
	audio := recovery.FillAudioGaps(items, gaps, audioSamplesPerPacket(manifest), uint32(len(srcLayout.Channels)))

	sourceRate := uint32(model.AudioClockRate)
	if manifest.Audio != nil && manifest.Audio.SampleRate != 0 {
		sourceRate = manifest.Audio.SampleRate
	}
	clock := model.ClockMap{
		Name: "rtp-audio-to-media", SourceRate: sourceRate,
		Media:       model.Timebase{Timescale: model.VideoTimescale},
		StartOffset: o.AVOffset(),
	}
	buildCfg := chunkBuildConfig{
		FramePath: p.Frames, TargetRate: orDefault32(o.TargetRate, sourceRate),
		SourceLayout: srcLayout, TargetLayout: dstLayout,
		Clock: clock, FrameDelta: model.VideoFrameDelta, Sync: true, Faults: faults,
	}

	var runErr error
	for ci := idx.NextChunk; ci < chunkCount; ci++ {
		start := ci * o.ChunkSize
		end := minInt(start+o.ChunkSize, o.Frames)
		data, _, err := buildRangeMP4(frames.Frames, start, end, audio.Blocks, buildCfg)
		if err != nil {
			var ee *model.ExitError
			if errors.As(err, &ee) && ee.Code == model.ExitSyncConfig {
				fillSummary(&sum, o, idx, rec, frames, jstats, audio, faults, manifest, inputSHA)
				sum.ValidFrames = validFrames
				sum.SyncErrors = faults.Count(model.FaultSync)
				sum.UnknownChannels = faults.Count(model.FaultUnknownChannel)
				sum.ExitCode = model.ExitSyncConfig
				sum.HaveResume = true
				sum.ResumedFromChunk = rec.ResumedFromChunk
				sum.HaveResumeTiming = true
				sum.ResumeElapsedMS = time.Since(started).Milliseconds()
				_ = WriteSummary(p.SummaryJSON, sum)
				return &RunResult{Summary: sum, Recovery: rec, Faults: faults}, err
			}
			return &RunResult{Summary: sum, Recovery: rec, Faults: faults}, err
		}
		if err := store.CommitChunk(idx, checkpoint.Chunk{Index: ci, StartFrame: start, EndFrame: end}, data); err != nil {
			return &RunResult{Summary: sum, Recovery: rec, Faults: faults}, err
		}
		if o.InterruptAfter > 0 && ci+1 >= o.InterruptAfter {
			runErr = model.NewExitError(model.ExitInterrupted, "simulated interruption after committed chunk boundary",
				model.Fault{Kind: model.FaultInterrupted, RecordIndex: int64(ci), Recoverable: true})
			break
		}
	}

	if runErr == nil {
		if err := finalizeOutput(p, idx, frames.Frames, audio.Blocks, buildCfg, store); err != nil {
			return &RunResult{Summary: sum, Recovery: rec, Faults: faults}, err
		}
	}

	outSHA := idx.OutputSHA256
	trackCount := 0
	if outSHA != "" {
		if b, err := os.ReadFile(p.FinalMP4); err == nil {
			if tracks, err := media.ProbeBytes(b); err == nil {
				trackCount = len(tracks)
			}
		}
	}
	fillSummary(&sum, o, idx, rec, frames, jstats, audio, faults, manifest, inputSHA)
	sum.ValidFrames = validFrames
	sum.CommittedChunks = idx.NextChunk
	sum.OutputPath = p.FinalMP4
	sum.OutputSHA256 = outSHA
	sum.MP4Tracks = trackCount
	sum.HaveResume = true
	sum.ResumedFromChunk = rec.ResumedFromChunk
	sum.HaveResumeTiming = true
	sum.ResumeElapsedMS = time.Since(started).Milliseconds()
	if runErr != nil {
		sum.ExitCode = model.ExitInterrupted
	} else {
		sum.ExitCode = model.ExitOK
		sum.Completed = idx.Completed
	}
	_ = WriteSummary(p.SummaryJSON, sum)
	return &RunResult{Summary: sum, Recovery: rec, Faults: faults}, runErr
}

func finalizeOutput(p Paths, idx *checkpoint.Index, frames []recovery.RestoredFrame, audio []recovery.AudioBlock, cfg chunkBuildConfig, store *checkpoint.Store) error {
	if idx.Completed && idx.OutputSHA256 != "" {
		if got, err := checkpoint.HashFile(p.FinalMP4); err == nil && got == idx.OutputSHA256 {
			return nil
		}
	}
	data, _, err := buildRangeMP4(frames, 0, len(frames), audio, cfg)
	if err != nil {
		return err
	}
	tmp := p.FinalMP4 + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	sha := checkpoint.HashBytes(data)
	if err := os.Rename(tmp, p.FinalMP4); err != nil {
		return err
	}
	idx.OutputPath = p.FinalMP4
	idx.OutputSHA256 = sha
	idx.Completed = true
	return store.Save(idx)
}

func fillSummary(sum *report.Summary, o Options, idx *checkpoint.Index, rec checkpoint.Recovery,
	frames *recovery.FrameReport, js buffer.Stats, audio *recovery.AudioReport,
	faults *model.Collector, manifest FixtureManifest, inputSHA string) {
	sum.Seed = o.Seed
	sum.FrameTotal = o.Frames
	sum.ChunkSize = o.ChunkSize
	sum.ChunkCount = idx.ChunkCount
	sum.InputSHA256 = inputSHA
	sum.RecordTotal = o.Frames + faults.Count(model.FaultDuplicateFrame)
	sum.BadSHA = faults.Count(model.FaultBadSHA)
	sum.Truncated = faults.Count(model.FaultTruncated)
	sum.BadTimestamp = faults.Count(model.FaultBadTimestamp)
	sum.FrameDuplicates = faults.Count(model.FaultDuplicateFrame)
	sum.LatePackets = js.Late
	sum.DuplicatePackets = js.Duplicate
	sum.OutOfOrderPackets = js.OutOfOrder
	sum.LostPackets = js.Lost
	sum.JitterEvents = js.JitterEvents
	sum.ConcealedPackets = audio.ConcealedPackets
	sum.ColorConflicts = faults.Count(model.FaultColorConflict)
	sum.SyncErrors = faults.Count(model.FaultSync)
	sum.UnknownChannels = faults.Count(model.FaultUnknownChannel)
	sum.CommittedChunks = idx.NextChunk
	if rec.RolledBackFrom > idx.NextChunk || rec.InputMismatch {
		sum.RolledBackFromChunk = rec.RolledBackFrom
	}
	sum.IndexCorrupt = rec.IndexCorrupt
	if frames != nil {
		sum.PresentFrames = frames.Present
		sum.MissingFrames = frames.Missing
	}
	if manifest.Frames != nil {
		sum.LocationErrorMax = locationError(faults.All(), manifest.Frames)
	}
	if manifest.Audio != nil {
		sum.AudioPackets = manifest.Audio.CanonicalPackets
		sum.StoredAudioRecords = manifest.Audio.StoredRecords
	}
}

func audioSamplesPerPacket(m FixtureManifest) uint32 {
	if m.Audio != nil && m.Audio.SamplesPerPacket != 0 {
		return m.Audio.SamplesPerPacket
	}
	return 480
}

func orDefault32(v, d uint32) uint32 {
	if v == 0 {
		return d
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
