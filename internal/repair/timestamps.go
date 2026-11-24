package repair

import (
	"fmt"

	"media-archiver/internal/media"
)

// Policy tells the repair stage how to classify and handle bad timestamps.
type Policy struct {
	MaxJumpFrames    int64 `json:"max_jump_frames"`
	RepairMissing    bool  `json:"repair_missing"`
	RepairDuplicate  bool  `json:"repair_duplicate"`
	RepairBackward   bool  `json:"repair_backward"`
	RepairExcessJump bool  `json:"repair_excess_jump"`
}

func DefaultPolicy() Policy {
	return Policy{
		MaxJumpFrames:    8,
		RepairMissing:    true,
		RepairDuplicate:  true,
		RepairBackward:   true,
		RepairExcessJump: true,
	}
}

// Decision is an auditable timestamp repair decision.
type Decision struct {
	BlockIndex         int    `json:"block_index"`
	SourceOffset       int64  `json:"source_offset"`
	Kind               string `json:"kind"`
	Action             string `json:"action"`
	Reason             string `json:"reason"`
	OriginalStartNS    *int64 `json:"original_start_ns"`
	RepairedStartNS    int64  `json:"repaired_start_ns"`
	OriginalStartFrame *int64 `json:"original_start_frame"`
	RepairedStartFrame int64  `json:"repaired_start_frame"`
}

// TimelineResult contains canonical blocks and all repair decisions.
type TimelineResult struct {
	Blocks    []media.CanonicalBlock `json:"blocks"`
	Decisions []Decision             `json:"decisions"`
}

// RepairTimeline converts declared timestamps to frame positions and restores
// a continuous frame timeline according to policy. Missing timestamps continue
// from the preceding block's end.
func RepairTimeline(raw []media.RawBlock, tb media.Timebase, policy Policy) (*TimelineResult, error) {
	if policy.MaxJumpFrames < 0 {
		return nil, media.NewError(media.CodeInvalidConfig, media.StageTransform, -1, "max_jump_frames must be non-negative", nil)
	}
	out := &TimelineResult{Blocks: make([]media.CanonicalBlock, 0, len(raw))}
	var expectedFrame int64
	var lastDeclaredNS *int64
	var lastDeclaredFrame *int64

	for _, b := range raw {
		originalNS := copyInt64Ptr(nil)
		var originalFrame *int64
		kind := ""
		repairEnabled := false

		if b.TimestampPresent {
			ns := b.TimestampNS
			originalNS = &ns
			f := tb.NSToFrame(ns)
			originalFrame = &f

			switch {
			case lastDeclaredNS != nil && ns == *lastDeclaredNS:
				kind = "duplicate_timestamp"
				repairEnabled = policy.RepairDuplicate
			case lastDeclaredFrame != nil && f == *lastDeclaredFrame && f < expectedFrame:
				kind = "duplicate_frame_timestamp"
				repairEnabled = policy.RepairDuplicate
			case f < expectedFrame:
				kind = "backward_timestamp"
				repairEnabled = policy.RepairBackward
			case f > expectedFrame+policy.MaxJumpFrames:
				kind = "excess_jump_timestamp"
				repairEnabled = policy.RepairExcessJump
			case f != expectedFrame:
				kind = "alignment_snap"
				repairEnabled = true
			}
		} else {
			kind = "missing_timestamp"
			repairEnabled = policy.RepairMissing
		}

		startFrame := expectedFrame
		action := "continuous"
		if kind != "" {
			if repairEnabled {
				action = "repaired_to_continuity"
			} else if originalFrame != nil {
				startFrame = *originalFrame
				action = "preserved_by_policy"
			} else {
				return nil, media.NewError(media.CodeInvalidConfig, media.StageTransform, b.DataOffset,
					fmt.Sprintf("cannot preserve missing timestamp for block %d", b.Index), nil)
			}
			out.Decisions = append(out.Decisions, Decision{
				BlockIndex:         b.Index,
				SourceOffset:       b.DataOffset,
				Kind:               kind,
				Action:             action,
				Reason:             reasonFor(kind),
				OriginalStartNS:    originalNS,
				RepairedStartNS:    tb.FramesToNS(startFrame),
				OriginalStartFrame: originalFrame,
				RepairedStartFrame: startFrame,
			})
		}

		frameCount := b.CompleteFrames
		block := media.CanonicalBlock{
			Index:               b.Index,
			ChunkOffset:         b.ChunkOffset,
			OriginalOffset:      b.DataOffset,
			FrameCount:          frameCount,
			StartFrame:          startFrame,
			EndFrame:            startFrame + frameCount,
			StartNS:             tb.FramesToNS(startFrame),
			EndNS:               tb.FramesToNS(startFrame + frameCount),
			OriginalTimestampNS: originalNS,
			TimestampPresent:    b.TimestampPresent,
			Bad:                 b.Truncated,
			CorruptOffset:       b.CorruptOffset,
			CorruptionReason:    b.CorruptionReason,
		}
		if kind != "" {
			block.RepairCodes = []string{kind + ":" + action}
		}
		out.Blocks = append(out.Blocks, block)

		expectedFrame = block.EndFrame
		if b.TimestampPresent {
			ns := b.TimestampNS
			f := tb.NSToFrame(ns)
			lastDeclaredNS = &ns
			lastDeclaredFrame = &f
		} else {
			lastDeclaredNS = nil
			lastDeclaredFrame = nil
		}
	}
	return out, nil
}

func copyInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func reasonFor(kind string) string {
	switch kind {
	case "missing_timestamp":
		return "missing timestamp continued from previous block end"
	case "duplicate_timestamp", "duplicate_frame_timestamp":
		return "duplicate timestamp replaced with contiguous frame position"
	case "backward_timestamp":
		return "backward timestamp replaced with contiguous frame position"
	case "excess_jump_timestamp":
		return "jump exceeded configured frame tolerance and was made contiguous"
	case "alignment_snap":
		return "sub-frame or small offset aligned to integer frame boundary"
	default:
		return "timestamp normalized"
	}
}
