package mediacore

import (
	"crypto/sha256"
	"fmt"
)

// SegmentDigest is the hash of one segment's retained media.
type SegmentDigest struct {
	Segment  int    `json:"segment"`
	TrackID  uint32 `json:"track_id"`
	First    int    `json:"first_sample"`
	Count    int    `json:"count"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	Complete bool   `json:"complete"`
	Missing  []Gap  `json:"missing,omitempty"`
}

// SegmentHashes is the deterministic per-segment hash set of one container.
type SegmentHashes struct {
	Digest   InputDigest     `json:"digest"`
	Segments []SegmentDigest `json:"segments"`
	Combined string          `json:"combined"`
}

// HashSegments streams the samples of every planned segment and hashes them.
// It reads with a single reusable reader per track, so payload never stays
// resident beyond one sample. Segments are keyed by their byte range and
// sample metadata; a partially delivered segment is marked Complete=false.
func HashSegments(c *Container, plan SegmentPlan, digest InputDigest, missing []Gap) (*SegmentHashes, error) {
	if c == nil {
		return nil, fail(KindIndex, "segment-hash", "nil container")
	}
	if len(plan.Segments) == 0 {
		return nil, fail(KindIndex, "segment-hash", "segment plan is empty")
	}
	out := &SegmentHashes{Digest: digest}
	readers := map[uint32]*SampleReader{}
	defer func() {
		for _, r := range readers {
			r.Close()
		}
	}()

	combined := sha256.New()
	for _, seg := range plan.Segments {
		tr := c.TrackByID(seg.TrackID)
		if tr == nil {
			return nil, fail(KindIndex, "segment-hash", "segment %d refers to unknown track %d", seg.Index, seg.TrackID)
		}
		if seg.First < 0 || seg.Count < 0 || seg.First+seg.Count > tr.SampleCount() {
			return nil, fail(KindIndex, "segment-hash", "segment %d range [%d,%d) exceeds track %d samples", seg.Index, seg.First, seg.First+seg.Count, seg.TrackID)
		}
		r := readers[tr.ID]
		if r == nil {
			var err error
			r, err = NewSampleReader(c.Path, tr)
			if err != nil {
				return nil, err
			}
			readers[tr.ID] = r
		}
		h := sha256.New()
		var bytes int64
		complete := len(missingForSegment(missing, seg.TrackID)) == 0
		fmt.Fprintf(h, "seg=%d track=%d first=%d count=%d\n", seg.Index, seg.TrackID, seg.First, seg.Count)
		for i := seg.First; i < seg.First+seg.Count; i++ {
			s := tr.Samples[i]
			if _, err := r.ReadSample(i, h); err != nil {
				return nil, err
			}
			fmt.Fprintf(h, "|dts=%d dur=%d cts=%d sync=%t size=%d\n", s.DTS, s.Duration, s.CTS, s.Sync, s.Size)
			bytes += s.Size
		}
		full := fmt.Sprintf("%x", h.Sum(nil))
		sd := SegmentDigest{
			Segment: seg.Index, TrackID: seg.TrackID,
			First: seg.First, Count: seg.Count, Bytes: bytes,
			SHA256: full, Complete: complete,
			Missing: missingForSegment(missing, seg.TrackID),
		}
		out.Segments = append(out.Segments, sd)
		fmt.Fprintf(combined, "%d:%s:%t\n", sd.Segment, sd.SHA256, sd.Complete)
	}
	out.Combined = fmt.Sprintf("%x", combined.Sum(nil))
	return out, nil
}

// missingForSegment returns gaps that overlap the segment's track, so a
// caller can tell why a segment may not verify.
func missingForSegment(gaps []Gap, trackID uint32) []Gap {
	var out []Gap
	for _, g := range gaps {
		if g.TrackID == trackID && g.State == GapMissing {
			out = append(out, g)
		}
	}
	return out
}

// Compare reports whether two hash sets agree segment by segment. It is the
// check behind "repeat runs produce identical hashes".
func (h *SegmentHashes) Compare(other *SegmentHashes) error {
	if other == nil {
		return fail(KindDigest, "segment-hash", "no comparison target")
	}
	if h.Combined != other.Combined {
		return fail(KindDigest, "segment-hash", "combined hash %s != %s", short(h.Combined), short(other.Combined))
	}
	if len(h.Segments) != len(other.Segments) {
		return fail(KindDigest, "segment-hash", "segment count %d != %d", len(h.Segments), len(other.Segments))
	}
	for i := range h.Segments {
		a, b := h.Segments[i], other.Segments[i]
		if a.SHA256 != b.SHA256 {
			return fail(KindDigest, "segment-hash", "segment %d hash %s != %s", i, short(a.SHA256), short(b.SHA256))
		}
	}
	return nil
}

// ResumePoint is the first segment a restarted pipeline must process.
type ResumePoint struct {
	Digest      InputDigest
	Segment     int
	ScannedSegs int
	Reason      string
}

// ResumeFrom finds where to continue after an interruption. It trusts only
// hashes computed over the same input digest and the same segment boundary;
// anything else restarts from segment zero and says why.
func (h *SegmentHashes) ResumeFrom(prev *SegmentHashes) ResumePoint {
	if prev == nil {
		return ResumePoint{Digest: h.Digest, Segment: 0, Reason: "no prior run"}
	}
	if prev.Digest.Content != h.Digest.Content || prev.Digest.Size != h.Digest.Size {
		return ResumePoint{Digest: h.Digest, Segment: 0, Reason: "input digest changed"}
	}
	done := len(prev.Segments)
	if done > len(h.Segments) {
		done = len(h.Segments)
	}
	for i := 0; i < done; i++ {
		if prev.Segments[i].SHA256 != h.Segments[i].SHA256 || !prev.Segments[i].Complete {
			return ResumePoint{Digest: h.Digest, Segment: i, ScannedSegs: i, Reason: "segment hash mismatch"}
		}
	}
	if done == len(h.Segments) {
		return ResumePoint{Digest: h.Digest, Segment: done, ScannedSegs: done, Reason: "already complete"}
	}
	return ResumePoint{Digest: h.Digest, Segment: done, ScannedSegs: done, Reason: "continuing from digest-bound segment"}
}
