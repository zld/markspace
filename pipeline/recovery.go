package pipeline

import (
	"fmt"

	"example.com/media-kernel/media"
	"example.com/media-kernel/provenance"
	"example.com/media-kernel/state"
)

// RecoveryReport contains hashes immediately before simulated interruption and
// the corresponding hashes after a fresh process loads the checkpoint.
type RecoveryReport struct {
	InterruptedAfter int               `json:"interrupted_after"`
	ResumedFrom      int               `json:"resumed_from"`
	BeforeHashes     map[int]string    `json:"before_hashes"`
	AfterHashes      map[int]string    `json:"after_hashes"`
	Checkpoint       *state.Checkpoint `json:"checkpoint,omitempty"`
}

// SimulateInterruptAndResume writes a checkpoint at a safe segment boundary,
// drops the in-memory checkpoint, loads it from disk like a new process, and
// completes remaining segments. Existing segment hashes are verified before
// the checkpoint is replaced.
func SimulateInterruptAndResume(store *state.Store, result *Result, paramsHash string, interruptAfter int) (*RecoveryReport, error) {
	if result == nil || store == nil {
		return nil, media.NewError("pipeline.recovery", media.CodeInvalidArgument, "store and result are required", nil)
	}
	if interruptAfter < 1 || interruptAfter > len(result.Segments) {
		interruptAfter = min(2, len(result.Segments))
	}

	cp := state.NewCheckpoint(result.InputHash, paramsHash)
	cp.Stage = state.StageIndexed
	if err := store.SaveChecked(cp, result.InputHash, paramsHash); err != nil {
		return nil, err
	}

	before := map[int]string{}
	for i := 0; i < interruptAfter; i++ {
		seg := result.Segments[i]
		if err := cp.RecordSegment(toSegmentState(seg)); err != nil {
			return nil, err
		}
		if err := store.SaveChecked(cp, result.InputHash, paramsHash); err != nil {
			return nil, err
		}
		before[i] = seg.SHA256
	}

	// Simulate a new OS process: only the file-backed store survives.
	loaded, err := store.Load()
	if err != nil {
		return nil, err
	}
	if err := loaded.Validate(result.InputHash, paramsHash); err != nil {
		return nil, err
	}
	report := &RecoveryReport{
		InterruptedAfter: interruptAfter,
		ResumedFrom:      loaded.SafeSegmentIndex,
		BeforeHashes:     before,
		AfterHashes:      map[int]string{},
	}

	resumeAt := loaded.SafeSegmentIndex
	for _, seg := range result.Segments {
		if seg.Index < resumeAt {
			prior, ok := loaded.Segments[seg.Index]
			if !ok || prior.SHA256 != seg.SHA256 {
				return nil, media.NewError("pipeline.recovery", media.CodeHashMismatch,
					fmt.Sprintf("completed segment %d hash changed", seg.Index), nil)
			}
			continue
		}
		if err := loaded.RecordSegment(toSegmentState(seg)); err != nil {
			return nil, err
		}
		if err := store.SaveChecked(loaded, result.InputHash, paramsHash); err != nil {
			return nil, err
		}
	}
	loaded.Stage = state.StageCompleted
	if len(result.Segments) > 0 {
		loaded.Offset = result.Segments[len(result.Segments)-1].ByteRange.End
	}
	if err := store.SaveChecked(loaded, result.InputHash, paramsHash); err != nil {
		return nil, err
	}

	for i := range before {
		report.AfterHashes[i] = loaded.Segments[i].SHA256
	}
	report.Checkpoint = loaded
	return report, nil
}

// VerifyIdempotentRetry reloads a checkpoint and ensures every deterministic
// segment hash produced by another run matches the durable record.
func VerifyIdempotentRetry(store *state.Store, result *Result, paramsHash string) error {
	cp, err := store.Load()
	if err != nil {
		return err
	}
	if err := cp.Validate(result.InputHash, paramsHash); err != nil {
		return err
	}
	if cp.Stage != state.StageCompleted {
		return media.NewError("pipeline.recovery", media.CodeCheckpoint,
			fmt.Sprintf("checkpoint stage=%s want completed", cp.Stage), nil)
	}
	if len(cp.Segments) != len(result.Segments) {
		return media.NewError("pipeline.recovery", media.CodeHashMismatch,
			fmt.Sprintf("checkpoint segments=%d current=%d", len(cp.Segments), len(result.Segments)), nil)
	}
	for _, seg := range result.Segments {
		if !cp.IsComplete(seg.Index, seg.SHA256) {
			return media.NewError("pipeline.recovery", media.CodeHashMismatch,
				fmt.Sprintf("idempotent retry mismatch at segment %d", seg.Index), nil)
		}
	}
	return nil
}

func toSegmentState(seg provenance.SegmentSummary) state.SegmentState {
	return state.SegmentState{
		Index:     seg.Index,
		ID:        seg.ID,
		SHA256:    seg.SHA256,
		EndOffset: seg.ByteRange.End,
	}
}
