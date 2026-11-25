// Package recover turns corruption and boundary failures into stable states.
package recover

import (
	"errors"

	"rtptrace/internal/manifest"
	"rtptrace/internal/segment"
	"rtptrace/internal/timeline"
)

// Recovery states are stable strings consumed by evidence and acceptance tests.
const (
	StateOK                   = "ok"
	StateRecoveryRequired     = "recovery_required"
	StateRecoveredEditClamped = "recovered-edit-clamped"
	StateRecoveredPrefix      = "recovered-prefix"
	StateFailed               = "failed"
	StateBlocked              = "blocked"
)

// Item is one concrete failure or recovery action.
type Item struct {
	Code           string `json:"code"`
	TrackID        uint32 `json:"track_id"`
	FailureSample  uint32 `json:"failure_sample,omitempty"`
	RecoveryState  string `json:"recovery_state"`
	Action         string `json:"action,omitempty"`
	Message        string `json:"message,omitempty"`
	TimeErrorTicks int64  `json:"time_error_ticks,omitempty"`
}

// CorruptionItem maps a manifest corruption error to a recovery state.
func CorruptionItem(err error, allowRecovery bool) Item {
	var c *manifest.CorruptionError
	if errors.As(err, &c) {
		state := StateRecoveryRequired
		action := "quarantine damaged sample table; emit report with exact location"
		if allowRecovery && c.Code == "truncated-stbl" {
			state = StateRecoveredPrefix
			action = "retain contiguous sample prefix and fail segments beyond it"
		}
		if c.Recoverable && allowRecovery {
			state = StateRecoveredEditClamped
		}
		return Item{
			Code: c.Code, TrackID: c.TrackID, FailureSample: c.Sample,
			RecoveryState: state, Action: action, Message: c.Message,
		}
	}
	if err != nil {
		return Item{Code: "internal", RecoveryState: StateFailed, Message: err.Error()}
	}
	return Item{Code: "none", RecoveryState: StateOK}
}

// BoundaryItem converts a failed segment boundary to an evidence item.
func BoundaryItem(b segment.Boundary) Item {
	code := "boundary-failed"
	if b.State == segment.BoundaryMissingKey {
		code = "missing-keyframe"
	}
	return Item{
		Code: code, TrackID: b.TrackID, FailureSample: b.FailureSample,
		RecoveryState: StateFailed,
		Action:        "target presentation boundary retained; downstream decode start withheld",
		Message:       b.Message,
	}
}

// EditAdjustment records one exact out-of-range edit clamp.
type EditAdjustment struct {
	EditIndex             int           `json:"edit_index"`
	Before                timeline.Edit `json:"before"`
	After                 timeline.Edit `json:"after"`
	OriginalEndMediaTicks int64         `json:"original_end_media_ticks"`
	MediaDurationTicks    int64         `json:"media_duration_ticks"`
	OverflowMediaTicks    int64         `json:"overflow_media_ticks"`
	TimeErrorTicks        int64         `json:"time_error_ticks"`
}

// EditReport is the recovery result for one track's edit list.
type EditReport struct {
	TrackID     uint32                `json:"track_id"`
	State       string                `json:"recovery_state"`
	Issues      []timeline.RangeIssue `json:"issues,omitempty"`
	Adjustments []EditAdjustment      `json:"adjustments,omitempty"`
	Edits       timeline.EditList     `json:"edits"`
	Message     string                `json:"message,omitempty"`
}

// RecoverEditList clamps an out-of-range non-empty edit to available media.
// When allowRecovery is false it returns the issue without modifying edits.
func RecoverEditList(trackID uint32, in timeline.EditList, mediaDurationTicks int64, allowRecovery bool) EditReport {
	issues := in.ValidateMediaRange(mediaDurationTicks)
	rep := EditReport{TrackID: trackID, State: StateOK, Issues: issues, Edits: in}
	if len(issues) == 0 {
		return rep
	}
	if !allowRecovery {
		rep.State = StateRecoveryRequired
		rep.Message = "edit list exceeds media sample table; rerun with --recover to clamp"
		return rep
	}

	cloned := in
	cloned.Edits = append([]timeline.Edit(nil), in.Edits...)
	for _, issue := range issues {
		if issue.EditIndex < 0 || issue.EditIndex >= len(cloned.Edits) {
			rep.State = StateFailed
			rep.Message = "range issue references missing edit index"
			return rep
		}
		e := cloned.Edits[issue.EditIndex]
		availableMedia := mediaDurationTicks - e.MediaTime
		if e.MediaTime < 0 || availableMedia <= 0 {
			rep.State = StateFailed
			rep.Message = "edit starts at or beyond media duration; no deterministic media prefix exists"
			return rep
		}
		// Largest movie-tick duration whose mapped media duration is in range.
		newDuration := availableMedia * int64(in.MovieTimescale) / int64(in.MediaTimescale)
		if newDuration <= 0 {
			rep.State = StateFailed
			rep.Message = "not enough media duration for one movie-timescale tick"
			return rep
		}
		before := e
		originalMediaDuration := e.SegmentDuration * int64(in.MediaTimescale) / int64(in.MovieTimescale)
		originalEnd := e.MediaTime + originalMediaDuration
		timeError := issue.OverflowTicks
		e.SegmentDuration = newDuration
		cloned.Edits[issue.EditIndex] = e
		rep.Adjustments = append(rep.Adjustments, EditAdjustment{
			EditIndex: issue.EditIndex, Before: before, After: e,
			OriginalEndMediaTicks: originalEnd, MediaDurationTicks: mediaDurationTicks,
			OverflowMediaTicks: issue.OverflowTicks, TimeErrorTicks: timeError,
		})
	}
	rep.Edits = cloned
	rep.State = StateRecoveredEditClamped
	rep.Message = "out-of-range edit duration clamped to available media without moving other edits"
	return rep
}

// Primary returns the first concrete failure item and stable aggregate fields.
func Primary(items []Item) (track uint32, sample uint32, state string) {
	state = StateOK
	for _, it := range items {
		if it.RecoveryState != StateOK {
			return it.TrackID, it.FailureSample, it.RecoveryState
		}
	}
	return 0, 0, state
}
