// Package fingerprint produces stable content digests for segments and groups
// identical content without losing the sources that produced it.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"sort"

	"mediaregress/internal/media"
	"mediaregress/internal/split"
	"mediaregress/internal/timeline"
	"mediaregress/internal/track"
)

// Algorithm labels the digest construction. Changing how a digest is computed
// must change this string so old and new fingerprints never compare equal.
const Algorithm = "segment-sha256/1"

// Status classifies a segment's comparability.
type Status string

const (
	// StatusComplete means the segment holds at least one full comparison
	// window and its digest may be compared with other complete segments.
	StatusComplete Status = "complete"
	// StatusIncomplete means the segment is shorter than one comparison
	// window. It is never reported as a duplicate, even when two incomplete
	// segments are byte-identical, because a fragment cannot prove sameness.
	StatusIncomplete Status = "incomplete"
	// StatusUnreadable means the segment's bytes could not be read.
	StatusUnreadable Status = "unreadable"
)

// Result is the fingerprint of one segment.
type Result struct {
	SegmentID    string       `json:"segment"`
	Track        string       `json:"track"`
	Kind         track.Kind   `json:"kind"`
	Window       media.Window `json:"window"`
	Digest       string       `json:"digest"`
	Bytes        int64        `json:"bytes"`
	Status       Status       `json:"status"`
	Comparable   bool         `json:"comparable"`
	Duplicate    bool         `json:"duplicate"`
	DuplicateOf  string       `json:"duplicate_of,omitempty"`
	Algorithm    string       `json:"algorithm"`
	ParseVersion string       `json:"parse_version"`
	SourceOffset int64        `json:"source_offset"`
	Stage        media.Stage  `json:"stage"`
}

// Group collects every source of identical content.
type Group struct {
	Digest   string         `json:"digest"`
	Segments []string       `json:"segments"`
	Tracks   []string       `json:"tracks"`
	Ranges   []media.Window `json:"ranges"`
	Primary  string         `json:"primary"`
}

// Duplicates are the groups with more than one member.
func Duplicates(results []Result) []Group {
	byDigest := map[string][]Result{}
	for _, r := range results {
		if !r.Comparable || r.Status != StatusComplete {
			continue
		}
		byDigest[r.Digest] = append(byDigest[r.Digest], r)
	}
	var out []Group
	for digest, list := range byDigest {
		if len(list) < 2 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].SegmentID < list[j].SegmentID })
		g := Group{Digest: digest, Primary: list[0].SegmentID}
		seenTrack := map[string]bool{}
		for _, r := range list {
			g.Segments = append(g.Segments, r.SegmentID)
			if !seenTrack[r.Track] {
				seenTrack[r.Track] = true
				g.Tracks = append(g.Tracks, r.Track)
			}
			g.Ranges = append(g.Ranges, r.Window)
		}
		sort.Strings(g.Tracks)
		sort.Slice(g.Ranges, func(i, j int) bool {
			if g.Ranges[i].Start != g.Ranges[j].Start {
				return g.Ranges[i].Start < g.Ranges[j].Start
			}
			return g.Ranges[i].End < g.Ranges[j].End
		})
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Digest < out[j].Digest })
	return out
}

// Compute digests every segment. minWindow is the smallest content length that
// may be compared; shorter segments are marked incomplete. Each segment's bytes
// are hashed with a bounded buffer.
func Compute(path string, tl *timeline.Timeline, segs []split.Segment, minWindow media.Ticks) ([]Result, []*media.Error, error) {
	if tl == nil {
		return nil, nil, media.NewError(media.ClassDependency, media.StageFingerprint, "", -1, "no timeline")
	}
	if minWindow <= 0 {
		return nil, nil, media.Errorf(media.ClassInvalidTimebase, media.StageFingerprint, "", -1,
			"comparison window %d must be positive", minWindow)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, media.WrapError(media.ClassCorruptInput, media.StageFingerprint, "", 0, "open input", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, media.WrapError(media.ClassCorruptInput, media.StageFingerprint, "", 0, "stat input", err)
	}
	tracks := map[string]*timeline.TrackTimeline{}
	for _, tt := range tl.Tracks {
		tracks[tt.TrackID] = tt
	}
	var results []Result
	var errs []*media.Error
	for _, seg := range segs {
		tt := tracks[seg.Track]
		if tt == nil {
			res := Result{
				SegmentID: seg.ID, Track: seg.Track, Kind: seg.Kind, Window: seg.Window,
				Status: StatusUnreadable, Algorithm: Algorithm, Stage: media.StageFingerprint,
				ParseVersion: seg.ParseVersion,
			}
			results = append(results, res)
			errs = append(errs, media.Errorf(media.ClassDependency, media.StageFingerprint, seg.Track,
				seg.SourceOffset, "segment %s has no timeline track", seg.ID))
			continue
		}
		res := Result{
			SegmentID: seg.ID, Track: seg.Track, Kind: seg.Kind, Window: seg.Window,
			Algorithm: Algorithm, ParseVersion: seg.ParseVersion,
			SourceOffset: seg.SourceOffset, Stage: media.StageFingerprint,
		}
		if tt.Truncated && seg.Window.End > tt.TruncatedAt {
			res.Status = StatusUnreadable
			results = append(results, res)
			errs = append(errs, media.Errorf(media.ClassTruncatedInput, media.StageFingerprint, seg.Track,
				maxInt64(tt.TruncatedOffset, 0), "segment %s reaches into truncated data", seg.ID))
			continue
		}
		digest, n, herr := hashSamples(f, fi.Size(), tt, seg)
		if herr != nil {
			res.Status = StatusUnreadable
			results = append(results, res)
			errs = append(errs, herr)
			continue
		}
		res.Digest = digest
		res.Bytes = n
		// Comparability is decided by content length, not by segment index:
		// a short trailing segment is incomplete even when it is the only one.
		if seg.Window.Duration() < minWindow {
			res.Status = StatusIncomplete
			res.Comparable = false
		} else {
			res.Status = StatusComplete
			res.Comparable = true
		}
		results = append(results, res)
	}
	markDuplicates(results)
	sort.Slice(results, func(i, j int) bool { return results[i].SegmentID < results[j].SegmentID })
	return results, errs, nil
}

// RecomputeDuplicates clears and re-derives duplicate flags. It is exported so
// a resumed run, which reuses stored per-segment results, makes the same
// duplicate decisions as an uninterrupted run.
func RecomputeDuplicates(results []Result) {
	for i := range results {
		results[i].Duplicate = false
		results[i].DuplicateOf = ""
	}
	markDuplicates(results)
}

// markDuplicates flags every member after the first of a complete group.
func markDuplicates(results []Result) {
	first := map[string]string{}
	for i := range results {
		r := &results[i]
		if r.Status != StatusComplete {
			continue
		}
		if id, ok := first[r.Digest]; ok {
			r.Duplicate = true
			r.DuplicateOf = id
			continue
		}
		first[r.Digest] = r.SegmentID
	}
}

// hashSamples hashes the segment's sample bytes in order with a fixed buffer.
// Only the content and its length are covered: two segments in different time
// ranges with identical bytes must produce the same digest so the dedup stage
// can recognise them. Position stays in the result as the source range.
func hashSamples(f *os.File, fileSize int64, tt *timeline.TrackTimeline, seg split.Segment) (string, int64, *media.Error) {
	h := sha256.New()
	h.Write([]byte(Algorithm))
	buf := make([]byte, 64<<10)
	var total int64
	for i := seg.StartSample; i < seg.EndSample && i < len(tt.Samples); i++ {
		s := tt.Samples[i]
		if s.Offset < 0 || s.Offset+int64(s.Size) > fileSize {
			return "", 0, media.Errorf(media.ClassTruncatedInput, media.StageFingerprint, seg.Track,
				fileSize, "segment %s needs bytes past end of file", seg.ID)
		}
		sr := io.NewSectionReader(f, s.Offset, int64(s.Size))
		n, err := io.CopyBuffer(h, sr, buf)
		if err != nil {
			return "", 0, media.WrapError(media.ClassCorruptInput, media.StageFingerprint, seg.Track,
				s.Offset, "cannot hash sample", err)
		}
		if n != int64(s.Size) {
			return "", 0, media.Errorf(media.ClassTruncatedInput, media.StageFingerprint, seg.Track,
				s.Offset+n, "short read while hashing sample")
		}
		total += n
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
