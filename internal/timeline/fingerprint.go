package timeline

import (
	"fmt"
	"strings"

	"medcheck/internal/model"
)

// Fingerprint renders the view as a stable, order-independent string of its
// deterministic content (frame ordering, boundaries and water mark). Two views
// built from the same frame set in different arrival orders must match.
func (v *View) Fingerprint() string {
	if v == nil {
		return "nil"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "wm=%d;max=%d;n=%d;", v.WatermarkMicros, v.MaxEventMicros, v.FrameCount)
	for _, f := range v.Ordered {
		valid := 0
		if f.Valid {
			valid = 1
		}
		fmt.Fprintf(&sb, "f(%d,%d,%d,%d,%d,%d,%d)",
			f.Track, f.Source, f.Seq,
			f.EventTime.MicrosOr(f.EventTime.Ticks),
			f.Offset.Start, f.Offset.End, valid)
	}
	for _, b := range v.Boundaries {
		fmt.Fprintf(&sb, "b(%d,%d,%d,%d,%d,%d,err=%d)",
			b.Track, b.Source, b.StartMicros, b.EndMicros,
			b.MinOffset.Start, b.MaxOffset.End, len(b.Errors))
	}
	return sb.String()
}

// ValidFrames returns only checksum-valid, error-free frames in deterministic
// order. Downstream key frame indexing must consume only these.
func (v *View) ValidFrames() []*model.Frame {
	if v == nil {
		return nil
	}
	out := make([]*model.Frame, 0, len(v.Ordered))
	for _, f := range v.Ordered {
		if f.Valid && f.Err == nil {
			out = append(out, f)
		}
	}
	return out
}
