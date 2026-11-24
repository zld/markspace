package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"media-archiver/internal/archive"
	idxpkg "media-archiver/internal/index"
	"media-archiver/internal/media"
	"media-archiver/internal/repair"
	"media-archiver/internal/verify"
	"media-archiver/internal/wav"
)

type Config struct {
	WorkDir        string
	Resume         bool
	Processing     ProcessingConfig
	InterruptAfter media.Stage
	LookupFrame    *int64
}

func DefaultProcessing(seed int64, injection string) ProcessingConfig {
	return ProcessingConfig{
		Seed:            seed,
		Injection:       injection,
		Policy:          repair.DefaultPolicy(),
		MaxBlockBytes:   wav.DefaultMaxBlockBytes,
		MaxIndexEntries: 100_000,
	}
}

// Run executes or resumes the deterministic parse → transform → index → verify flow.
func Run(cfg Config) (*Report, error) {
	if cfg.WorkDir == "" {
		return nil, media.NewError(media.CodeInvalidConfig, "", -1, "workdir is required", nil)
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, media.NewError(media.CodeInternal, "", -1, "create workdir", err)
	}
	if cfg.Resume {
		existing, loadErr := loadManifest(cfg.WorkDir)
		if loadErr != nil {
			me := media.NewError(media.CodeInvalidConfig, "", -1, "cannot resume without manifest", loadErr)
			return &Report{WorkDir: cfg.WorkDir, Status: "failed", ErrorCode: me.Code, Stage: me.Stage, CorruptOffset: me.Offset}, me
		}
		cfg.Processing = existing.Config
	}
	if cfg.Processing.MaxBlockBytes <= 0 {
		cfg.Processing.MaxBlockBytes = wav.DefaultMaxBlockBytes
	}
	if cfg.Processing.MaxIndexEntries <= 0 {
		cfg.Processing.MaxIndexEntries = 100_000
	}

	inputRel := filepath.Join("input", "sample.wav")
	inputPath := filepath.Join(cfg.WorkDir, inputRel)
	inputSHA, inputSize, err := hashFileStat(inputPath)
	if err != nil {
		me := media.NewError(media.CodeInvalidConfig, media.StageParse, -1, "input sample is unavailable", err)
		return &Report{WorkDir: cfg.WorkDir, Status: "failed", ErrorCode: me.Code, Stage: me.Stage, CorruptOffset: me.Offset}, me
	}
	configSHA := hashCanonicalOrPanic(cfg.Processing)

	man, err := loadManifest(cfg.WorkDir)
	sameSource := err == nil && man.InputSHA256 == inputSHA && man.ConfigSHA256 == configSHA
	if cfg.Resume && err != nil {
		me := media.NewError(media.CodeInvalidConfig, "", -1, "cannot resume without manifest", err)
		return &Report{WorkDir: cfg.WorkDir, Status: "failed", ErrorCode: me.Code, Stage: me.Stage, CorruptOffset: me.Offset}, me
	}
	if err != nil || !sameSource {
		man = newManifest(cfg.Processing, inputSHA, inputSize, configSHA, 0)
		_ = saveManifest(cfg.WorkDir, man)
		// A changed source/config invalidates downstream reuse; remove stale
		// random-access and verification artifacts before recomputation.
		removeArtifact(cfg.WorkDir, indexPath)
		removeArtifact(cfg.WorkDir, indexChecksumPath)
		removeArtifact(cfg.WorkDir, statsPath)
	} else {
		cfg.Processing = man.Config
	}

	rep := &Report{WorkDir: cfg.WorkDir, Status: man.Status, InputSHA256: inputSHA}
	for _, s := range man.Stages {
		if s.Status == "completed" {
			rep.ReusedStages = append(rep.ReusedStages, string(s.Name))
		}
	}

	format := man.Format
	var blocks []media.CanonicalBlock
	var pcm []byte
	var provenance *repair.ProvenanceDocument

	if man.stage(media.StageTransform).Status == "completed" && format.BlockAlign != 0 {
		blocks, pcm, err = loadCanonicalBlocks(cfg.WorkDir, format.FrameByteCount())
		if err == nil {
			if data, pErr := os.ReadFile(artifactPath(cfg.WorkDir, provenancePath)); pErr == nil {
				_ = json.Unmarshal(data, &provenance)
			}
		}
	}
	if blocks == nil {
		removeArtifact(cfg.WorkDir, canonicalBlocksPath)
		removeArtifact(cfg.WorkDir, canonicalPCMPath)
		doc, rErr := wav.Read(inputPath, wav.Options{MaxBlockBytes: cfg.Processing.MaxBlockBytes})
		if rErr != nil {
			return failManifest(cfg.WorkDir, man, rep, rErr)
		}
		format = doc.Format
		man.Format = format
		if _, wErr := writeJSONArtifact(cfg.WorkDir, parseReportPath, doc); wErr != nil {
			return failManifest(cfg.WorkDir, man, rep, wErr)
		}
		man.setStage(media.StageParse, "completed", false, "")
		rep.RecomputedStages = append(rep.RecomputedStages, string(media.StageParse))
		if sErr := saveManifest(cfg.WorkDir, man); sErr != nil {
			return failManifest(cfg.WorkDir, man, rep, sErr)
		}
		if cfg.InterruptAfter == media.StageParse {
			return interrupt(cfg.WorkDir, man, rep, media.StageParse)
		}

		tb := media.Timebase{SampleRate: format.Rate()}
		timeline, aErr := repair.Assemble(format, doc.Blocks, tb, cfg.Processing.Policy)
		if aErr != nil {
			return failManifest(cfg.WorkDir, man, rep, aErr)
		}
		blocks = timeline.Blocks
		pcm = repair.ConcatPCM(blocks)
		audioSHA := media.SHA256Hex(pcm)
		inputRecord := repair.InputRecord{
			Path: inputRel, SHA256: inputSHA, Size: inputSize,
			Seed: cfg.Processing.Seed, Injection: cfg.Processing.Injection, ConfigSHA: configSHA,
		}
		provenance = repair.NewProvenance(inputRecord, cfg.Processing.Policy, timeline)
		if _, wErr := writeJSONArtifact(cfg.WorkDir, canonicalBlocksPath, blocks); wErr != nil {
			return failManifest(cfg.WorkDir, man, rep, wErr)
		}
		if _, wErr := writeBytesArtifact(cfg.WorkDir, canonicalPCMPath, pcm); wErr != nil {
			return failManifest(cfg.WorkDir, man, rep, wErr)
		}
		provSHA, wErr := writeJSONArtifact(cfg.WorkDir, provenancePath, provenance)
		if wErr != nil {
			return failManifest(cfg.WorkDir, man, rep, wErr)
		}
		man.AudioSHA256 = audioSHA
		man.setStage(media.StageTransform, "completed", false, provSHA)
		rep.RecomputedStages = append(rep.RecomputedStages, string(media.StageTransform))
		rep.Repairs = timeline.Decisions
		rep.RepairCount = len(timeline.Decisions)
		if sErr := saveManifest(cfg.WorkDir, man); sErr != nil {
			return failManifest(cfg.WorkDir, man, rep, sErr)
		}
		if cfg.InterruptAfter == media.StageTransform {
			return interrupt(cfg.WorkDir, man, rep, media.StageTransform)
		}
	} else if provenance != nil {
		rep.Repairs = provenance.Decisions
		rep.RepairCount = len(provenance.Decisions)
	}

	idx, indexSHA, iErr := loadIndexArtifact(cfg.WorkDir, man.IndexSHA256)
	indexUsable := iErr == nil && idx != nil && len(idx.Entries) == len(blocks) && idx.TotalFrames == man.TotalFrames
	if !indexUsable {
		opts := idxpkg.BuildOptions{MaxEntries: cfg.Processing.MaxIndexEntries}
		idx, iErr = idxpkg.Build(blocks, format.Rate(), opts)
		if iErr != nil {
			return failManifest(cfg.WorkDir, man, rep, iErr)
		}
		indexSHA, iErr = writeIndexArtifact(cfg.WorkDir, idx)
		if iErr != nil {
			return failManifest(cfg.WorkDir, man, rep, iErr)
		}
		if man.stage(media.StageIndex).Status == "completed" || man.IndexSHA256 != "" {
			rep.IndexRebuilt = true
		}
		man.IndexSHA256 = indexSHA
		man.IndexEntries = len(idx.Entries)
		man.TotalFrames = idx.TotalFrames
		man.setStage(media.StageIndex, "completed", false, indexSHA)
		rep.RecomputedStages = append(rep.RecomputedStages, string(media.StageIndex))
		if sErr := saveManifest(cfg.WorkDir, man); sErr != nil {
			return failManifest(cfg.WorkDir, man, rep, sErr)
		}
	} else {
		indexSHA = man.IndexSHA256
	}
	if cfg.InterruptAfter == media.StageIndex {
		rep.IndexSHA256 = indexSHA
		rep.IndexEntries = len(idx.Entries)
		return interrupt(cfg.WorkDir, man, rep, media.StageIndex)
	}

	if cfg.LookupFrame != nil {
		hit, lErr := idx.LookupFrame(*cfg.LookupFrame)
		if lErr != nil {
			return failManifest(cfg.WorkDir, man, rep, lErr)
		}
		rep.Lookup = &hit
	}

	badBlocks := repair.CountBadBlocks(blocks)
	verifyWasCompleted := man.stage(media.StageVerify).Status == "completed"
	stats, vErr := verify.Compute(format, pcm, badBlocks)
	if vErr != nil {
		return failManifest(cfg.WorkDir, man, rep, vErr)
	}
	if !verifyWasCompleted {
		rep.RecomputedStages = append(rep.RecomputedStages, string(media.StageVerify))
	}
	alignment := int64(0)
	if provenance != nil {
		for i, bp := range provenance.Blocks {
			if i >= len(idx.Entries) {
				break
			}
			e := idx.Entries[i]
			alignment = max64(alignment, abs64(bp.TimeRangeNS[0]-e.StartNS), abs64(bp.TimeRangeNS[1]-e.EndNS))
		}
	}
	tb := media.Timebase{SampleRate: format.Rate()}
	man.Stats = &stats
	man.ManifestAlignmentNS = alignment
	man.AlignmentBoundNS = tb.AlignmentBoundNS()
	if _, wErr := writeJSONArtifact(cfg.WorkDir, statsPath, stats); wErr != nil {
		return failManifest(cfg.WorkDir, man, rep, wErr)
	}

	rep.Stats = &stats
	rep.TargetBitrateBPS = stats.TargetBitrateBPS
	rep.ActualBitrateBPS = stats.ActualBitrateBPS
	rep.BitrateDeviationPct = stats.BitrateDeviationPct
	rep.Frames = stats.Frames
	rep.Channels = stats.Channels
	rep.Peak = stats.Peak
	rep.ClippedSamples = stats.ClippedSamples
	rep.BadBlocks = stats.BadBlocks
	rep.ManifestAlignmentNS = alignment
	rep.IndexEntries = len(idx.Entries)
	rep.IndexSHA256 = indexSHA
	rep.AudioSHA256 = man.AudioSHA256
	if rep.AudioSHA256 == "" {
		rep.AudioSHA256 = media.SHA256Hex(pcm)
	}

	if badBlocks > 0 {
		offset, reason := firstCorruption(blocks)
		me := media.NewError(media.CodeTruncated, media.StageParse, offset, reason, nil)
		man.setStage(media.StageVerify, "failed", false, "")
		return failManifest(cfg.WorkDir, man, rep, me)
	}
	if !stats.BitrateWithinTolerance(0.1) {
		me := media.NewError(media.CodeValidation, media.StageVerify, -1, "bitrate deviation exceeds 0.1 percent", nil)
		man.setStage(media.StageVerify, "failed", false, "")
		return failManifest(cfg.WorkDir, man, rep, me)
	}

	man.setStage(media.StageVerify, "completed", false, "")
	man.Status = "completed"
	man.Failure = nil
	data, mErr := media.MarshalCanonicalJSON(man)
	if mErr != nil {
		return failManifest(cfg.WorkDir, man, rep, mErr)
	}
	if wErr := media.WriteFileAtomic(manifestPath(cfg.WorkDir), data, 0o644); wErr != nil {
		return failManifest(cfg.WorkDir, man, rep, wErr)
	}
	manifestSHA := hashBytes(data)
	_ = media.WriteFileAtomic(artifactPath(cfg.WorkDir, "manifest.sha256"),
		[]byte(manifestSHA+"  manifest.json\n"), 0o644)
	archivePath := artifactPath(cfg.WorkDir, "archive/manifest.mp4")
	if aErr := archive.WriteManifestArchive(archivePath, data); aErr != nil {
		return failManifest(cfg.WorkDir, man, rep, aErr)
	}
	if readBack, aErr := archive.ReadManifestArchive(archivePath); aErr != nil {
		return failManifest(cfg.WorkDir, man, rep, aErr)
	} else if string(readBack) != string(data) {
		return failManifest(cfg.WorkDir, man, rep, media.NewError(media.CodeInternal, media.StageVerify, -1, "MP4 manifest read-back mismatch", nil))
	}

	rep.Status = "completed"
	rep.ManifestSHA256 = manifestSHA
	return rep, nil
}
