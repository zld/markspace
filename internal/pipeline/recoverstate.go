package pipeline

import (
	"liveregress/internal/manifest"
	"liveregress/internal/recover"
)

// recoverState builds the checkpoint record for a fully verified segment.
func recoverState(layer string, seg manifest.Segment, rep SegmentReport) recover.SegmentState {
	return recover.SegmentState{
		Layer:        layer,
		Sequence:     seg.Sequence,
		Path:         seg.Path,
		InputDigest:  rep.InputSHA256,
		OutputDigest: rep.FileSHA256,
		Bytes:        rep.Bytes,
		Samples:      rep.Samples,
		Complete:     rep.Complete,
		Keyframes:    rep.Keyframes,
	}
}
