package mediacore

import "fmt"

// resumeSegment reports where a run interrupted after the first complete
// segment would continue, for the terminal report.
func resumeSegment(h *SegmentHashes) int {
	if h == nil || len(h.Segments) < 2 {
		return 0
	}
	prefix := *h
	prefix.Segments = append([]SegmentDigest(nil), h.Segments[:1]...)
	return h.ResumeFrom(&prefix).Segment
}

// verifyResume proves restart logic end to end: no prior run starts at zero, a
// complete prefix continues at the next boundary, and a digest change forces a
// full restart.
func verifyResume(h *SegmentHashes) (bool, string) {
	if h == nil || len(h.Segments) == 0 {
		return false, "no segments"
	}
	if rp := h.ResumeFrom(nil); rp.Segment != 0 {
		return false, "nil prior did not restart at segment 0"
	}
	if len(h.Segments) >= 2 {
		prefix := *h
		prefix.Segments = append([]SegmentDigest(nil), h.Segments[:1]...)
		if rp := h.ResumeFrom(&prefix); rp.Segment != 1 {
			return false, fmt.Sprintf("one-segment prefix resumed at %d, want 1", rp.Segment)
		}
	}
	changed := *h
	changed.Digest = InputDigest{
		Content: "0000000000000000000000000000000000000000000000000000000000000000",
		Size:    h.Digest.Size,
	}
	rp := h.ResumeFrom(&changed)
	if rp.Segment != 0 || rp.Reason != "input digest changed" {
		return false, fmt.Sprintf("digest change resumed at %d (%s)", rp.Segment, rp.Reason)
	}
	return true, "prefix continues at boundary; digest change restarts at 0"
}
