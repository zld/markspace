package pipeline

import (
	"encoding/json"
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

type pipelineState struct {
	Item       *manifest.Item       `json:"item"`
	Inputs     []*parsedInput       `json:"inputs"`
	Candidates []metadata.Candidate `json:"candidates"`
	FP         source.Fingerprint   `json:"fingerprint"`
	Expect     Expectation          `json:"expectation"`
}

func (p *Pipeline) ProcessSample(s *samplegen.Sample) (*manifest.Item, error) {
	return p.ProcessPaths(s.StableKey, s.ID, s.Kind, Expectation{
		DamageOffset: s.ExpectedDamageOffset,
		Code:         s.ExpectedCode,
	}, s.Paths)
}

func stageIndex(stage task.Stage) int {
	for i, s := range task.Stages {
		if s == stage {
			return i
		}
	}
	return 0
}

func shouldRun(stage task.Stage, plan task.ResumePlan) bool {
	return stageIndex(stage) >= stageIndex(plan.ResumeStage)
}

func (p *Pipeline) ProcessPaths(key, sampleID, kind string, expect Expectation, paths []string) (*manifest.Item, error) {
	inputs, err := readInputs(paths)
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

	st := p.restoreState(plan, contentHash, inputs, expect)
	if shouldRun(task.StageFingerprint, plan) {
		st.FP = source.Fingerprint{Path: key, ContentSHA256: contentHash}
		if err := p.checkpoint(key, task.StageFingerprint, st); err != nil {
			return nil, err
		}
	}
	if shouldRun(task.StageParse, plan) {
		parseInputs(st.Inputs)
		if err := p.checkpoint(key, task.StageParse, st); err != nil {
			return nil, err
		}
	}
	if shouldRun(task.StageMetadata, plan) || st.Item == nil {
		p.buildMetadataState(key, sampleID, kind, paths, st)
		if err := p.checkpoint(key, task.StageMetadata, st); err != nil {
			return nil, err
		}
	}
	if shouldRun(task.StageValidate, plan) {
		validateIntegrity(st.Inputs, st.Item)
		addTimeDeviations(st.Item)
		if err := p.checkpoint(key, task.StageValidate, st); err != nil {
			return nil, err
		}
	}
	if shouldRun(task.StageRecover, plan) || st.Item.Recovery == nil {
		st.Item.Recovery = buildRecovery(st.Inputs, st.Item, plan)
		addExpectedDeviation(st.Item, st.Expect)
		if err := p.checkpoint(key, task.StageRecover, st); err != nil {
			return nil, err
		}
	}

	st.Item.ErrorCount = len(st.Item.Errors)
	st.Item.ConflictCount = len(st.Item.Metadata.Conflicts)
	p.index.Upsert(index.Record{
		TaskKey: key, Path: st.Item.InputPath, ContentSHA256: st.FP.ContentSHA256,
		StructureSHA256: st.FP.StructureSHA256, MetadataVersion: st.FP.MetadataVersion,
		TrackCount: len(st.Item.Tracks), ErrorCount: st.Item.ErrorCount,
	})
	if err := p.index.Save(); err != nil {
		return nil, err
	}
	if err := p.checkpoint(key, task.StageManifest, st); err != nil {
		return nil, err
	}
	if err := p.checkpoint(key, task.StageDone, st); err != nil {
		return nil, err
	}
	return st.Item, nil
}

func (p *Pipeline) restoreState(plan task.ResumePlan, contentHash string, inputs []*parsedInput, expect Expectation) *pipelineState {
	fresh := &pipelineState{Inputs: inputs, Expect: expect}
	if plan.LastValid == nil {
		return fresh
	}
	var restored pipelineState
	if err := json.Unmarshal(plan.LastValid.Payload, &restored); err != nil {
		return fresh
	}
	if restored.FP.ContentSHA256 != contentHash || len(restored.Inputs) != len(inputs) {
		return fresh
	}
	restored.Expect = expect
	return &restored
}

func (p *Pipeline) buildMetadataState(key, sampleID, kind string, paths []string, st *pipelineState) {
	item := &manifest.Item{StableKey: key, SampleID: sampleID, Kind: kind, SubmissionCount: 1}
	for _, in := range st.Inputs {
		item.InputPaths = append(item.InputPaths, in.Path)
		item.Tracks = append(item.Tracks, in.Doc.Tracks...)
		item.Errors = append(item.Errors, in.Doc.Errors...)
		for _, tr := range in.Doc.Tracks {
			st.Candidates = append(st.Candidates, metadata.Candidate{Source: string(in.Doc.Format), TrackID: tr.ID, Meta: tr.Metadata})
		}
		mv := "0"
		if len(in.Doc.Tracks) > 0 {
			mv = strconv.FormatUint(uint64(in.Doc.Tracks[0].Metadata.MetadataVersion), 10)
		}
		item.Fingerprints = append(item.Fingerprints, source.FromBytes(in.Path, in.Data, in.Doc.StructureSHA256, mv))
	}
	item.InputPath = paths[0]
	item.Format = st.Inputs[0].Doc.Format
	if len(st.Inputs) > 1 {
		item.Format = "MIXED"
	}
	item.InputByteRange.End = int64(len(st.Inputs[0].Data))
	resolved := metadata.Resolve(st.Candidates)
	item.Metadata = &resolved
	st.FP = bundleFingerprint(key, st.Inputs, strconv.FormatUint(uint64(resolved.Metadata.MetadataVersion), 10))
	item.SHA256 = st.FP.ContentSHA256
	item.Fingerprint = &st.FP
	st.Item = item

	old, hadOld := p.index.Get(key)
	change := source.ChangeSet{SamePath: hadOld}
	if hadOld {
		change.ContentReplaced = old.ContentSHA256 != st.FP.ContentSHA256
		change.StructureChanged = old.StructureSHA256 != st.FP.StructureSHA256
		change.MetadataChanged = old.MetadataVersion != st.FP.MetadataVersion
	}
	p.tracker.Observe(st.FP)
	keySet := cache.KeysFor(key, st.FP)
	if hadOld {
		p.cache.InvalidateForChange(keySet, change)
		p.index.Invalidate(key, change)
	}
	for _, ck := range []string{keySet.Parsed, keySet.Metadata, keySet.Recovery, keySet.Manifest} {
		p.cache.Put(ck, true)
	}
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
