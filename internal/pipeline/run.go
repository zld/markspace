package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"mediad/internal/clock"
	"mediad/internal/diagnose"

	"mediad/internal/core"
	"mediad/internal/index"
	"mediad/internal/ingest"
	"mediad/internal/recovery"
	"mediad/internal/validate"
)

// Stage names in execution order. A resumed run starts at the first stage not
// present in the checkpoint.
const (
	StageIngest    = "ingest"
	StageNormalize = "normalize"
	StageIndex     = "index"
	StageDiagnose  = "diagnose"
	StageValidate  = "validate"
)

// StageOrder lists stages in the order they commit.
func StageOrder() []string {
	return []string{StageIngest, StageNormalize, StageIndex, StageDiagnose, StageValidate}
}

// Runner executes the pipeline for one input set.
type Runner struct {
	Root   string
	Params Parameters
	// Checkpoint is the journal path. When CheckpointRoot is set the path must
	// resolve inside it.
	Checkpoint     string
	CheckpointRoot string
	Limits         ingest.Limits
}

// Errors surfaced to the CLI. Each maps to a specific exit code.
var (
	// ErrDamaged reports that input damage was found and a report was produced.
	ErrDamaged = errors.New("pipeline: damaged input detected")
	// ErrCheckpoint reports a checkpoint that cannot be trusted.
	ErrCheckpoint = errors.New("pipeline: checkpoint rejected")
)

// Run executes or resumes the pipeline for the named assets under Root.
func (r *Runner) Run(names []string) (*Output, error) {
	limits := r.Limits
	if limits.MaxAssetBytes <= 0 {
		limits = ingest.DefaultLimits()
		if r.Params.MaxAssetBytes > 0 {
			limits.MaxAssetBytes = r.Params.MaxAssetBytes
		}
	}
	if r.Params.ClockRate <= 0 {
		r.Params.ClockRate = 90000
	}
	if r.Params.MasterTrack == "" {
		r.Params.MasterTrack = "video"
	}
	assets, err := ingest.LoadManifest(r.Root, names, limits)
	if err != nil {
		return nil, fmt.Errorf("pipeline: load: %w", err)
	}
	inputDigest := ingest.InputDigest(assets)
	paramsDigest, err := paramsDigest(r.Params)
	if err != nil {
		return nil, err
	}
	// The checkpoint belongs to the output area, not the generated input tree,
	// so it is not confined to Root; the CLI resolves and confines it itself.
	store, err := recovery.NewStore(r.CheckpointRoot, r.Checkpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCheckpoint, err)
	}
	resumed, err := store.Begin(inputDigest, paramsDigest)
	if err != nil {
		if errors.Is(err, recovery.ErrDigestMismatch) || errors.Is(err, recovery.ErrCorruptCheckpoint) {
			return nil, fmt.Errorf("%w: %v", ErrCheckpoint, err)
		}
		return nil, err
	}

	out := &Output{}
	for _, a := range assets {
		out.Assets = append(out.Assets, AssetSummary{Name: a.Name, Format: a.Format, Size: a.Size(), Digest: a.Digest})
	}
	// A stage is reused only when its payload can be decoded and every earlier
	// stage is also reused; a gap forces a full recompute from that point.
	reuse := resumed
	for _, stage := range StageOrder() {
		payload, ok, err := store.Lookup(stage)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCheckpoint, err)
		}
		if reuse && ok {
			if r.restore(stage, payload, out) {
				out.Resumed = append(out.Resumed, stage)
				continue
			}
		}
		reuse = false
		cache, err := r.ensureCache(assets, out)
		if err != nil {
			return nil, err
		}
		if err := r.execute(stage, assets, out, cache); err != nil {
			return nil, err
		}
		if err := commitStage(store, stage, out); err != nil {
			return nil, err
		}
	}
	out.ReusedAll = len(out.Resumed) == len(StageOrder())
	out.Stages, err = stageDigests(store)
	if err != nil {
		return nil, err
	}
	out.InputDigest = inputDigest
	out.ParamsDigest = paramsDigest
	// The summary checksum covers the whole report body, so it is computed
	// here from the same record stream the writer emits.
	out.Summary = summarize(out)
	digest, err := ComputeReportChecksum(out, r.Params)
	if err != nil {
		return nil, err
	}
	out.Summary.SHA256 = digest
	out.Damaged = out.Summary.CorruptRanges > 0 || out.Summary.Issues > 0
	if !out.Damaged {
		return out, nil
	}
	if out.Summary.InvariantsFailed > 0 {
		return out, fmt.Errorf("%w: %d invariant failures", ErrDamaged, out.Summary.InvariantsFailed)
	}
	return out, ErrDamaged
}

func paramsDigest(p Parameters) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return core.HashParts(data), nil
}

// restore decodes a committed stage payload into the output. It reports false
// when the payload is structurally unusable, which triggers recomputation.
func (r *Runner) restore(stage string, payload []byte, out *Output) bool {
	switch stage {
	case StageIngest:
		var v IngestStage
		if json.Unmarshal(payload, &v) != nil {
			return false
		}
		out.Ingest = &v
	case StageNormalize:
		var v NormalizeStage
		if json.Unmarshal(payload, &v) != nil {
			return false
		}
		out.Normalize = &v
	case StageIndex:
		var v IndexStage
		if json.Unmarshal(payload, &v) != nil {
			return false
		}
		out.Index = &v
	case StageDiagnose:
		var v DiagnoseStage
		if json.Unmarshal(payload, &v) != nil {
			return false
		}
		out.Diagnose = &v
	case StageValidate:
		var v ValidateStage
		if json.Unmarshal(payload, &v) != nil {
			return false
		}
		out.Validate = &v
	default:
		return false
	}
	return true
}

func commitStage(store *recovery.Store, stage string, out *Output) error {
	var v any
	var records int
	switch stage {
	case StageIngest:
		v, records = out.Ingest, len(out.Ingest.Cues)
	case StageNormalize:
		v, records = out.Normalize, len(out.Normalize.Points)
	case StageIndex:
		v, records = out.Index, len(out.Index.Indexes)
	case StageDiagnose:
		v, records = out.Diagnose, len(out.Diagnose.Ranges)
	case StageValidate:
		v, records = out.Validate, out.Validate.Result.Total
	}
	payload, err := core.CanonicalJSON(v)
	if err != nil {
		return err
	}
	return store.Commit(stage, payload, records)
}

func stageDigests(store *recovery.Store) ([]StageDigest, error) {
	stages := store.SortedStages()
	sort.SliceStable(stages, func(i, j int) bool { return stages[i].Name < stages[j].Name })
	out := make([]StageDigest, 0, len(stages))
	for _, st := range stages {
		out = append(out, StageDigest{Name: st.Name, Digest: st.Digest, RecordsThrough: st.RecordsThrough})
	}
	return out, nil
}

func summarize(out *Output) Summary {
	s := Summary{}
	if out.Ingest != nil {
		s.Cues = len(out.Ingest.Cues)
	}
	if out.Index != nil {
		for _, idx := range out.Index.Indexes {
			s.Frames += idx.FrameCount
			s.KeyframeGaps += len(idx.GapsTicks())
		}
	}
	if out.Diagnose != nil {
		s.CorruptRanges = len(out.Diagnose.Ranges)
		// The diagnose stage already aggregates every earlier finding, so it
		// is the single source of truth for the issue count.
		s.Issues = len(out.Diagnose.Issues)
	}
	if out.Validate != nil {
		s.InvariantsFailed = out.Validate.Result.Failed
	}
	s.OffsetErrorBytes = offsetError(out)
	return s
}

// offsetError measures how far each reported range start is from the injected
// ground truth carried on the pipeline's expectation list. The demo compares
// against fixtures; a normal run with no expectations reports zero.
func offsetError(out *Output) int64 {
	var total int64
	for _, pair := range out.OffsetChecks {
		total += pair.Err
	}
	return total
}

// ValidateOutput re-runs the invariant stage over an already completed output.
// The CLI uses it for `verify --report`.
func ValidateOutput(out *Output, sizes map[string]int64, formats map[string]string) *validate.Result {
	in := validate.Input{Sizes: sizes, AssetFormats: formats}
	if out.Normalize != nil {
		tl, err := rebuildTimeline(out.Normalize)
		if err != nil {
			// A normalize payload that cannot be rebuilt is itself a
			// validation failure, not a reason to skip the timeline checks.
			in.Issues = append(in.Issues, diagnose.Issue{
				Category: diagnose.CatInvariant, Stage: diagnose.StageValidate,
				Format: "report", Asset: "", Detail: "timeline rebuild failed: " + err.Error(),
			})
		} else {
			in.Timeline = tl
		}
	}
	if out.Index != nil {
		for _, idx := range out.Index.Indexes {
			ref := validate.IndexRef{
				Asset: idx.Asset, TrackID: idx.TrackID, Timescale: idx.Timescale,
				FrameCount: idx.FrameCount, MinGap: idx.MinGap, MaxGap: idx.MaxGap, VFR: idx.VFR,
			}
			for _, kf := range idx.Keyframes {
				ref.Keyframes = append(ref.Keyframes, validate.KeyRef{
					Number: kf.Number, DTS: kf.DTS, Span: kf.Span, Offset: kf.Offset, Gap: kf.Gap, Trusted: kf.Trusted,
				})
			}
			in.Indexes = append(in.Indexes, ref)
		}
		in.Issues = append(in.Issues, out.Index.Issues...)
	}
	if out.Ingest != nil {
		in.Issues = append(in.Issues, out.Ingest.Issues...)
		for _, d := range out.Ingest.Drifts {
			in.Drifts = append(in.Drifts, validate.DriftRef{
				Asset: d.Asset, Node: d.Node, Samples: d.Samples, Slope: d.Slope,
				Intercept: d.Intercept, RatePPB: d.RatePPB, Applied: d.Applied, Span: d.Span,
			})
		}
	}
	if out.Normalize != nil {
		in.Issues = append(in.Issues, out.Normalize.Issues...)
	}
	if out.Diagnose != nil {
		in.Issues = append(in.Issues, out.Diagnose.Issues...)
		for _, rg := range out.Diagnose.Ranges {
			in.Ranges = append(in.Ranges, validate.RangeRef{Asset: rg.Asset, Format: rg.Format, Span: rg.Span})
		}
	}
	return validate.All(in)
}

// rebuildTimeline reconstructs a sealed timeline from a normalize payload so
// verification does not need the original assets in memory.
func rebuildTimeline(ns *NormalizeStage) (*clock.Timeline, error) {
	if len(ns.Tracks) == 0 {
		return nil, errors.New("no tracks")
	}
	tl := &clock.Timeline{Master: ns.Tracks[0].Name, Tracks: ns.Tracks, Points: ns.Points, Wraps: ns.Wraps, Clamps: ns.Clamps}
	for i, p := range tl.Points {
		if i > 0 && p.Master.Cmp(tl.Points[i-1].Master) < 0 {
			return nil, fmt.Errorf("timeline regressed at point %d", i)
		}
	}
	return tl, nil
}

// keyframeIndexRefs converts parsed indexes for validation.
func keyframeIndexRefs(idxs []*index.Index) []validate.IndexRef {
	out := make([]validate.IndexRef, 0, len(idxs))
	for _, idx := range idxs {
		ref := validate.IndexRef{
			Asset: idx.Asset, TrackID: idx.TrackID, Timescale: idx.Timescale,
			FrameCount: idx.FrameCount, MinGap: idx.MinGap, MaxGap: idx.MaxGap, VFR: idx.VFR,
		}
		for _, kf := range idx.Keyframes {
			ref.Keyframes = append(ref.Keyframes, validate.KeyRef{
				Number: kf.Number, DTS: kf.DTS, Span: kf.Span, Offset: kf.Offset, Gap: kf.Gap, Trusted: kf.Trusted,
			})
		}
		out = append(out, ref)
	}
	return out
}
