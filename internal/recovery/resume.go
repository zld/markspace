// Package recovery computes the safe resume point from a durable journal.
package recovery

import (
	"archivecheck/internal/clock"
	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
	mp4scan "archivecheck/internal/mp4"
	"archivecheck/internal/plan"
	"archivecheck/internal/store"
)

// Resume describes where a process should continue after reading its log.
type Resume struct {
	Found           bool          `json:"found"`
	RecordID        string        `json:"record_id"`
	LastStage       plan.Stage    `json:"last_stage"`
	StartStage      plan.Stage    `json:"start_stage"`
	Completed       bool          `json:"completed"`
	Failed          bool          `json:"failed"`
	LayerCursors    map[int64]int `json:"layer_cursors"`
	CommittedArts   int           `json:"committed_artifacts"`
	TransitionCount int           `json:"transition_count"`
	Message         string        `json:"message"`
}

// FromJournal calculates the resume coordinates without replaying media.
func FromJournal(j *store.Journal) Resume {
	if j == nil {
		return Resume{
			StartStage:   plan.StagePendingScan,
			LayerCursors: map[int64]int{},
			Message:      "no journal; starting from pending_scan",
		}
	}
	r := Resume{
		Found:           true,
		RecordID:        j.RecordID,
		LastStage:       j.Stage,
		StartStage:      j.Stage,
		Completed:       j.Stage == plan.StageCompleted,
		Failed:          j.Stage == plan.StageFailed,
		LayerCursors:    map[int64]int{},
		CommittedArts:   len(j.Artifacts),
		TransitionCount: len(j.Transitions),
		Message:         "journal found; resuming from committed stage",
	}
	for bitrate, l := range j.Layers {
		r.LayerCursors[bitrate] = l.Cursor
	}
	if r.Failed {
		r.Message = "journal ends in failed stage; explicit retry resets to pending_scan"
	}
	return r
}

// RetryFailed legally moves a failed journal back to pending scan. Crash
// recovery never uses this path because a hard interruption does not append
// a failed transition.
func RetryFailed(j *store.Journal) (bool, error) {
	if j == nil || j.Stage != plan.StageFailed {
		return false, nil
	}
	if err := j.Transition(plan.StagePendingScan, "retry after classified failure"); err != nil {
		return false, err
	}
	return true, nil
}

// LayerCursor returns the independently committed cursor for one bitrate.
func LayerCursor(j *store.Journal, bitrate int64) int {
	if j == nil || j.Layers[bitrate] == nil {
		return 0
	}
	return j.Layers[bitrate].Cursor
}

// Position computes the requested/shared and actual keyframe recovery
// coordinates for one representation fragment.
func Position(scan *mp4scan.ScanResult, bitrate int64, fragmentIndex int, requestTick int64) (model.RecoveryPosition, error) {
	if scan == nil {
		return model.RecoveryPosition{}, apperrors.E(apperrors.KindRecovery, "missing scan result").WithBitrate(bitrate)
	}
	s, err := scan.RecoverKeyframe(requestTick)
	if err != nil {
		kind := apperrors.KindOf(err)
		if kind == "" {
			kind = apperrors.KindRecovery
		}
		return model.RecoveryPosition{}, apperrors.Wrap(kind, err).WithBitrate(bitrate)
	}
	actual := s.PTS
	return model.RecoveryPosition{
		Bitrate:       bitrate,
		FragmentIndex: fragmentIndex,
		RequestTick:   requestTick,
		ActualTick:    actual,
		DeltaTicks:    int64(clock.Abs(clock.Tick(requestTick), clock.Tick(actual))),
	}, nil
}

// NextRequestTick returns the deterministic cut point for fragment i. The
// valid fixture has one non-keyframe cut at 36,000 and an end boundary.
func NextRequestTick(base model.EditInterval, fragmentIndex, fragmentCount int) int64 {
	if fragmentCount <= 1 {
		return base.StartTick
	}
	step := (base.EndTick - base.StartTick) / int64(fragmentCount)
	return base.StartTick + step*int64(fragmentIndex)
}
