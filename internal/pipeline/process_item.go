package pipeline

import (
	"math"
	"strconv"

	"media-audit/internal/cache"
	auditerrors "media-audit/internal/errors"
	"media-audit/internal/index"
	"media-audit/internal/manifest"
	"media-audit/internal/metadata"
	"media-audit/internal/samplegen"
	"media-audit/internal/source"
	"media-audit/internal/task"
)

type Expectation struct {
	DamageOffset int64
	Code         auditerrors.Code
}

func (p *Pipeline) ProcessSample(s *samplegen.Sample) (*manifest.Item, error) {
	return p.ProcessPaths(s.StableKey, s.ID, s.Kind, Expectation{
		DamageOffset: s.ExpectedDamageOffset,
		Code:         s.ExpectedCode,
	}, s.Paths)
}

func (p *Pipeline) ProcessPaths(key, sampleID, kind string, expect Expectation, paths []string) (*manifest.Item, error) {
	inputs, err := readAndParse(paths)
	if err != nil {
		return nil, err
	}
	contentHash := bundleFingerprint(key, inputs, "").ContentSHA256
	if cached := p.cachedDone(key, contentHash); cached != nil {
		return cached, nil
	}
	if _, err := p.tasks.MarkRunning(key, paths); err != nil {
		return nil, err
	}
	plan, _ := p.tasks.ResumePlan(key)

	candidates := make([]metadata.Candidate, 0)
	item := &manifest.Item{StableKey: key, SampleID: sampleID, Kind: kind, SubmissionCount: 1}
	for _, in := range inputs {
		item.InputPaths = append(item.InputPaths, in.Path)
		item.Tracks = append(item.Tracks, in.Doc.Tracks...)
		item.Errors = append(item.Errors, in.Doc.Errors...)
		for _, tr := range in.Doc.Tracks {
			candidates = append(candidates, metadata.Candidate{Source: string(in.Doc.Format), TrackID: tr.ID, Meta: tr.Metadata})
		}
		mv := "0"
		if len(in.Doc.Tracks) > 0 {
			mv = strconv.FormatUint(uint64(in.Doc.Tracks[0].Metadata.MetadataVersion), 10)
		}
		item.Fingerprints = append(item.Fingerprints, source.FromBytes(in.Path, in.Data, in.Doc.StructureSHA256, mv))
	}
	item.InputPath = paths[0]
	item.Format = inputs[0].Doc.Format
	if len(inputs) > 1 {
		item.Format = "MIXED"
	}
	item.InputByteRange.Start = 0
	item.InputByteRange.End = int64(len(inputs[0].Data))

	resolved := metadata.Resolve(candidates)
	item.Metadata = &resolved
	fp := bundleFingerprint(key, inputs, strconv.FormatUint(uint64(resolved.Metadata.MetadataVersion), 10))
	item.SHA256 = fp.ContentSHA256
	item.Fingerprint = &fp

	old, hadOld := p.index.Get(key)
	change := source.ChangeSet{SamePath: hadOld}
	if hadOld {
		change.ContentReplaced = old.ContentSHA256 != fp.ContentSHA256
		change.StructureChanged = old.StructureSHA256 != fp.StructureSHA256
		change.MetadataChanged = old.MetadataVersion != fp.MetadataVersion
	}
	p.tracker.Observe(fp)
	keySet := cacheKeysFor(key, fp)
	if hadOld {
		p.cache.InvalidateForChange(keySet, change)
		p.index.Invalidate(key, change)
	}
	for _, ck := range []string{keySet.Parsed, keySet.Metadata, keySet.Recovery, keySet.Manifest} {
		p.cache.Put(ck, true)
	}
	if err := p.checkpoint(key, task.StageFingerprint, fp); err != nil {
		return nil, err
	}
	if err := p.checkpoint(key, task.StageParse, map[string]int{"inputs": len(inputs), "tracks": len(item.Tracks)}); err != nil {
		return nil, err
	}
	if err := p.checkpoint(key, task.StageMetadata, resolved); err != nil {
		return nil, err
	}

	validateIntegrity(inputs, item)
	addTimeDeviations(item)
	if err := p.checkpoint(key, task.StageValidate, map[string]int{"errors": len(item.Errors)}); err != nil {
		return nil, err
	}

	item.Recovery = buildRecovery(inputs, item, plan)
	addExpectedDeviation(item, expect)
	if err := p.checkpoint(key, task.StageRecover, item.Recovery); err != nil {
		return nil, err
	}

	item.ErrorCount = len(item.Errors)
	item.ConflictCount = len(resolved.Conflicts)
	p.index.Upsert(index.Record{
		TaskKey: key, Path: item.InputPath, ContentSHA256: fp.ContentSHA256,
		StructureSHA256: fp.StructureSHA256, MetadataVersion: fp.MetadataVersion,
		TrackCount: len(item.Tracks), ErrorCount: item.ErrorCount,
	})
	if err := p.index.Save(); err != nil {
		return nil, err
	}
	if err := p.checkpoint(key, task.StageManifest, item); err != nil {
		return nil, err
	}
	if err := p.checkpoint(key, task.StageDone, item); err != nil {
		return nil, err
	}
	return item, nil
}

func cacheKeysFor(key string, fp source.Fingerprint) cache.KeySet {
	return cache.KeysFor(key, fp)
}

func (p *Pipeline) checkpoint(key string, stage task.Stage, payload any) error {
	if _, err := p.tasks.SaveCheckpoint(key, stage, payload); err != nil {
		return err
	}
	_, err := p.tasks.CompleteStage(key, stage)
	return err
}

func addTimeDeviations(item *manifest.Item) {
	for _, tr := range item.Tracks {
		if tr.TimeRange == nil {
			continue
		}
		tickDelta := math.Max(math.Abs(tr.TimeRange.Start.ErrorTicks), math.Abs(tr.TimeRange.End.ErrorTicks))
		nsDelta := math.Max(math.Abs(tr.TimeRange.Start.ErrorNS), math.Abs(tr.TimeRange.End.ErrorNS))
		item.Deviations = append(item.Deviations,
			manifest.Deviation{Kind: "time-conversion", Unit: "unified_tick", Delta: tickDelta, MaxAllowed: 1, WithinTol: tickDelta <= 1},
			manifest.Deviation{Kind: "time-conversion", Unit: "nanosecond", Delta: nsDelta, WithinTol: true},
		)
	}
}

func addExpectedDeviation(item *manifest.Item, expect Expectation) {
	if expect.DamageOffset == 0 {
		return
	}
	var actual int64 = -1
	for _, e := range item.Errors {
		if expect.Code == "" || e.Code == expect.Code {
			actual = e.Offset
			break
		}
	}
	item.Deviations = append(item.Deviations, manifest.Deviation{
		Kind: "damage-offset", Unit: "byte",
		Expected: float64(expect.DamageOffset), Actual: float64(actual),
		Delta: float64(actual - expect.DamageOffset), WithinTol: actual == expect.DamageOffset,
	})
	if actual < 0 {
		item.Errors = append(item.Errors, auditerrors.New(auditerrors.CodeUnknownStructure, 0,
			"self-test expected %s at byte %d but no matching error was reported", expect.Code, expect.DamageOffset))
	}
}
