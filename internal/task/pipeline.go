package task

import (
	"audionorm/internal/audio"
	"audionorm/internal/ingest"
	"audionorm/internal/media"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// BudgetTolerance is the acceptance window around the target sample count.
const BudgetTolerancePct = 0.5

// Pipeline runs the full stage machine for one task and drives recovery.
type Pipeline struct {
	root    string
	decoder *audio.Decoder
}

// NewPipeline builds a pipeline rooted at work directory root.
func NewPipeline(root string) (*Pipeline, error) {
	dec, err := audio.NewDecoder()
	if err != nil {
		return nil, err
	}
	return &Pipeline{root: root, decoder: dec}, nil
}

// Input bundles everything needed to start or resume one task.
type Input struct {
	StreamID string
	Range    media.Range
	Spec     Spec
	// RTPIngest or FMP4Result: exactly one is used.
	RTPResult   *ingest.Result
	FMP4Result  *ingest.Result
	InputSHA256 string
	// DecodeSpec tells FFmpeg how to read the container.
	DecodeSpec audio.StreamSpec
	// ContainerData is the full muxed byte stream (Ogg/ADTS/Matroska)
	// handed to the decoder; packet payloads alone are not demuxable.
	ContainerData []byte
}

// Run executes the stage machine. On restart it verifies the checkpoint
// and reports which stages were reused versus recomputed.
func (p *Pipeline) Run(ctx context.Context, in Input) (*Checkpoint, *RunReport, error) {
	key := StableKey(in.StreamID, in.Range, in.Spec)
	report := &RunReport{TaskKey: key}

	cp, err := LoadCheckpoint(p.root, key)
	if errors.Is(err, ErrCheckpointNotFound) {
		cp = &Checkpoint{
			TaskKey: key, StreamID: in.StreamID, Range: in.Range, Spec: in.Spec,
			Stage: StageNew, InputSHA256: in.InputSHA256,
			DecoderVersion: audio.DecoderVersion,
			Stages:         map[Stage]*StageResult{},
		}
	} else if err != nil {
		return nil, report, err
	} else {
		// Existing task: conflict check then verify.
		if err := CheckConflict(cp, in.StreamID, in.Range, in.Spec, in.InputSHA256); err != nil {
			return nil, report, err
		}
		reusable, verr := cp.Verify(p.root)
		if verr != nil {
			report.CheckpointError = verr.Error()
		}
		report.ReusedStage = reusable
		if cp.InputChanged(in.InputSHA256) || reusable == StageNew {
			cp.Stages = map[Stage]*StageResult{}
			cp.Stage = StageNew
		}
	}

	result := in.RTPResult
	if result == nil {
		result = in.FMP4Result
	}
	if result == nil {
		return nil, report, errors.New("no ingest result supplied")
	}

	if cp.Stage == StageNew {
		if err := p.stageIngest(cp, result); err != nil {
			return cp, report, err
		}
		report.Recomputed = append(report.Recomputed, StageIngest)
	}
	if Index(cp.Stage) < Index(StageDecode) {
		if err := p.stageDecode(ctx, cp, in.DecodeSpec, in.ContainerData); err != nil {
			return cp, report, err
		}
		report.Recomputed = append(report.Recomputed, StageDecode)
	}
	if Index(cp.Stage) < Index(StageNormalize) {
		if err := p.stageNormalize(cp); err != nil {
			return cp, report, err
		}
		report.Recomputed = append(report.Recomputed, StageNormalize)
	}
	if Index(cp.Stage) < Index(StageSeal) {
		if err := p.stageSeal(cp, result); err != nil {
			return cp, report, err
		}
		report.Recomputed = append(report.Recomputed, StageSeal)
	}
	return cp, report, nil
}

func (p *Pipeline) stageIngest(cp *Checkpoint, result *ingest.Result) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return cp.RecordStage(p.root, StageIngest, "ingest.json", data)
}

func (p *Pipeline) stageDecode(ctx context.Context, cp *Checkpoint, spec audio.StreamSpec, container []byte) error {
	// The caller supplies the full muxed stream (Ogg/ADTS/Matroska); raw
	// codec packets are not demuxable on their own. Timeline ranges from
	// the ingest stage remain authoritative for gap accounting.
	switch spec.Container {
	case "matroska", "adts", "aac", "ogg":
	default:
		return fmt.Errorf("unsupported decode container %q", spec.Container)
	}
	if len(container) == 0 {
		return errors.New("empty decode container")
	}
	pcm, err := p.decoder.DecodeStream(ctx, spec, container)
	if err != nil {
		cp.Errors = append(cp.Errors, media.NewPendingError(media.ErrDecode, err.Error(), 0, 0))
		return err
	}
	// Enforce the 64 MiB PCM cache bound.
	if len(pcm.Samples)*4 > media.MaxPCMBytes {
		return errors.New("PCM cache exceeds 64 MiB bound")
	}
	return cp.RecordStage(p.root, StageDecode, "decode.f32le", pcm.EncodeF32LE())
}

func (p *Pipeline) stageNormalize(cp *Checkpoint) error {
	raw, err := readStagePayload(p.root, cp, StageDecode)
	if err != nil {
		return err
	}
	samples := bytesToF32(raw)
	stereo, err := audio.ToStereo(samples, 2)
	if err != nil {
		return err
	}
	out, rep := audio.NormalizeLoudness(stereo)
	cp.Loudness = &rep
	return cp.RecordStage(p.root, StageNormalize, "normalized.f32le", f32ToBytes(out))
}

func (p *Pipeline) stageSeal(cp *Checkpoint, result *ingest.Result) error {
	raw, err := readStagePayload(p.root, cp, StageNormalize)
	if err != nil {
		return err
	}
	samples := bytesToF32(raw)
	frames := len(samples) / 2
	cp.Samples = frames

	// Known gaps: locatable missing intervals. Unknown errors -> HOLD.
	var missing uint64
	for _, g := range result.Gaps {
		missing += g.When.Length()
	}
	cp.MissingSamples = missing
	cp.Errors = append(cp.Errors, result.Errors.All()...)

	target := cp.Range.Length()
	if target > 0 {
		cp.BudgetDevPct = float64(int64(frames)-int64(target)) / float64(target) * 100
	}

	hasUnknown := false
	for _, e := range result.Errors.All() {
		if !e.Locatable {
			hasUnknown = true
		}
	}
	switch {
	case hasUnknown:
		cp.Status = StatusHOLD
	case missing > 0 || len(result.Gaps) > 0:
		cp.Status = StatusPARTIAL
	default:
		cp.Status = StatusPASS
	}
	// Final output is sealed under a stable name with its hash recorded.
	if err := cp.RecordStage(p.root, StageSeal, "output.f32le", raw); err != nil {
		return err
	}
	_, err = SaveCheckpoint(p.root, cp)
	return err
}

// RunReport explains what a run reused versus recomputed.
type RunReport struct {
	TaskKey         string   `json:"task_key"`
	ReusedStage     Stage    `json:"reused_stage"`
	Recomputed      []Stage  `json:"recomputed"`
	CheckpointError string   `json:"checkpoint_error,omitempty"`
}

var _ = StageNew
