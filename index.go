package mediacore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// KeyframeEntry is one random-access point in a track timeline. Sample is a
// zero-based index into the track's sample table.
type KeyframeEntry struct {
	Sample int   `json:"sample"`
	DTS    int64 `json:"dts"`
	CTS    int64 `json:"cts"`
	Offset int64 `json:"offset"`
	Size   int64 `json:"size"`
}

// TrackIndex is the scan result for one track.
type TrackIndex struct {
	TrackID   uint32          `json:"track_id"`
	TargetID  uint32          `json:"target_id"`
	Kind      TrackKind       `json:"kind"`
	Codec     string          `json:"codec"`
	Timescale uint32          `json:"timescale"`
	Samples   int             `json:"samples"`
	Duration  int64           `json:"duration"`
	Bytes     int64           `json:"bytes"`
	Keyframes []KeyframeEntry `json:"keyframes"`
	Sizes     []int64         `json:"sizes"`
	TrackHash string          `json:"track_hash"`
	Preserved bool            `json:"preserved"`
}

// Segment is a contiguous sample range that starts on a keyframe.
type Segment struct {
	Index    int    `json:"index"`
	TrackID  uint32 `json:"track_id"` // source track whose timeline was split
	StartDTS int64  `json:"start_dts"`
	EndDTS   int64  `json:"end_dts"`
	First    int    `json:"first_sample"`
	Count    int    `json:"count"`
	Keyframe int    `json:"keyframe"` // index into TrackIndex.Keyframes
	Bytes    int64  `json:"bytes"`
}

// SegmentPlan is the deterministic segment split for one container.
type SegmentPlan struct {
	SegmentDuration int64     `json:"segment_duration"`
	Timescale       uint32    `json:"timescale"`
	Segments        []Segment `json:"segments"`
	Windows         int       `json:"windows"`
	Misses          int       `json:"misses"`
	Aligned         bool      `json:"aligned"`
}

// Index is a complete scan of one input, bound to its digest.
type Index struct {
	Digest          InputDigest  `json:"digest"`
	MovieTimescale  uint32       `json:"movie_timescale"`
	Tracks          []TrackIndex `json:"tracks"`
	Plan            SegmentPlan  `json:"plan"`
	SchemaVersion   int          `json:"schema_version"`
	KeyframeHitRate float64      `json:"keyframe_hit_rate"`
	SplitTrackID    uint32       `json:"split_track_id"`
	BuiltFrom       string       `json:"built_from"`
}

// IndexSchemaVersion is bumped whenever the on-disk index layout changes.
const IndexSchemaVersion = 1

// TrackByID returns the index for a source track.
func (idx *Index) TrackByID(id uint32) *TrackIndex {
	for i := range idx.Tracks {
		if idx.Tracks[i].TrackID == id {
			return &idx.Tracks[i]
		}
	}
	return nil
}

// TracksByTarget returns the index for a target track.
func (idx *Index) TracksByTarget(id uint32) *TrackIndex {
	for i := range idx.Tracks {
		if idx.Tracks[i].TargetID == id {
			return &idx.Tracks[i]
		}
	}
	return nil
}

// SegmentAt returns segment n, or a classified error when n is outside the
// plan. Execution refuses out-of-range keyframe indices rather than clamping.
func (idx *Index) SegmentAt(n int) (*Segment, error) {
	if n < 0 || n >= len(idx.Plan.Segments) {
		return nil, fail(KindIndex, "segment", "segment index %d out of range [0,%d)", n, len(idx.Plan.Segments))
	}
	return &idx.Plan.Segments[n], nil
}

// KeyframeAt returns keyframe k of a track by index.
func (t *TrackIndex) KeyframeAt(k int) (*KeyframeEntry, error) {
	if k < 0 || k >= len(t.Keyframes) {
		return nil, fail(KindIndex, "keyframe", "keyframe index %d out of range [0,%d) for track %d", k, len(t.Keyframes), t.TrackID)
	}
	return &t.Keyframes[k], nil
}

// ScanOptions controls index construction.
type ScanOptions struct {
	Map TrackMapConfig
	// SegmentDuration is expressed in movie timescale ticks. Zero produces a
	// single segment covering the whole track.
	SegmentDuration int64
}

// DefaultScanOptions maps tracks by kind and keeps unknown tracks intact.
func DefaultScanOptions() ScanOptions {
	return ScanOptions{
		Map: TrackMapConfig{
			Rules:       []TrackRule{{Kind: KindVideoTrack}, {Kind: KindAudioTrack}},
			KeepUnknown: true,
		},
		SegmentDuration: 0,
	}
}

// Scan builds an index for c. Only sample-table metadata is used; media
// payload is never read, so memory grows with sample count, not file size.
func Scan(c *Container, digest InputDigest, opts ScanOptions) (*Index, error) {
	if c == nil {
		return nil, fail(KindIndex, "scan", "nil container")
	}
	tm, err := BuildTrackMap(c, opts.Map)
	if err != nil {
		return nil, err
	}
	movieTimescale := c.MovieTimescale
	if movieTimescale == 0 {
		movieTimescale = 1000
	}
	idx := &Index{
		Digest:         digest,
		MovieTimescale: movieTimescale,
		SchemaVersion:  IndexSchemaVersion,
		BuiltFrom:      c.Path,
	}
	for i := range c.Tracks {
		src := &c.Tracks[i]
		target, _ := tm.TargetIDFor(src.ID)
		ti, err := scanTrack(src, target)
		if err != nil {
			return nil, err
		}
		idx.Tracks = append(idx.Tracks, *ti)
	}
	plan, splitID, err := buildSegmentPlan(idx.Tracks, movieTimescale, opts.SegmentDuration)
	if err != nil {
		return nil, err
	}
	idx.Plan = *plan
	idx.SplitTrackID = splitID
	idx.KeyframeHitRate = keyframeHitRate(plan)
	return idx, nil
}

func scanTrack(src *Track, target uint32) (*TrackIndex, error) {
	if src.Timescale == 0 {
		return nil, fail(KindIndex, "scan", "track %d has zero timescale", src.ID)
	}
	ti := &TrackIndex{
		TrackID:   src.ID,
		TargetID:  target,
		Kind:      src.Kind,
		Codec:     src.Codec,
		Timescale: src.Timescale,
		Samples:   len(src.Samples),
		Duration:  src.EndTicks - src.StartTicks,
		Preserved: src.Unknown,
		TrackHash: trackDigest(src),
	}
	var prevDTS int64
	for i, s := range src.Samples {
		if i > 0 && s.DTS <= prevDTS {
			return nil, fail(KindTimestamp, "scan", "track %d sample %d DTS %d is not strictly increasing (previous %d)", src.ID, i, s.DTS, prevDTS)
		}
		prevDTS = s.DTS
		ti.Bytes += s.Size
		ti.Sizes = append(ti.Sizes, s.Size)
		if !s.Sync {
			continue
		}
		ti.Keyframes = append(ti.Keyframes, KeyframeEntry{
			Sample: i, DTS: s.DTS, CTS: s.CTS, Offset: s.Offset, Size: s.Size,
		})
	}
	if len(ti.Keyframes) == 0 {
		return nil, fail(KindIndex, "scan", "track %d has no keyframes", src.ID)
	}
	return ti, nil
}

// trackDigest hashes per-sample metadata (not payload) so identical inputs
// always yield the same digest on any platform.
func trackDigest(src *Track) string {
	h := sha256.New()
	fmt.Fprintf(h, "id=%d kind=%s ts=%d codec=%s handler=%s n=%d end=%d\n",
		src.ID, src.Kind, src.Timescale, src.Codec, src.HandlerType, len(src.Samples), src.EndTicks-src.StartTicks)
	for _, s := range src.Samples {
		fmt.Fprintf(h, "%d|%d|%d|%d|%d|%t\n", s.Index, s.Offset, s.Size, s.DTS, s.Duration, s.Sync)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// buildSegmentPlan splits the longest video track (or the longest track when
// no video exists) into windows of SegmentDuration movie ticks. Each segment
// begins at the first keyframe inside its window; windows without a keyframe
// are recorded as misses and merged into the preceding segment. Absolute
// window positions are converted with one rational rounding each, so the
// boundary of window n never depends on window n-1.
func buildSegmentPlan(tracks []TrackIndex, timescale uint32, duration int64) (*SegmentPlan, uint32, error) {
	if len(tracks) == 0 {
		return nil, 0, fail(KindIndex, "plan", "index has no tracks")
	}
	chosen := -1
	for i := range tracks {
		if tracks[i].Kind != KindVideoTrack {
			continue
		}
		if chosen < 0 || tracks[i].Duration > tracks[chosen].Duration {
			chosen = i
		}
	}
	if chosen < 0 {
		for i := range tracks {
			if chosen < 0 || tracks[i].Duration > tracks[chosen].Duration {
				chosen = i
			}
		}
	}
	tr := tracks[chosen]
	plan := &SegmentPlan{SegmentDuration: duration, Timescale: timescale, Aligned: true, Windows: 1}
	if duration <= 0 || timescale == 0 || len(tr.Keyframes) <= 1 || tr.Duration <= 0 {
		plan.Segments = []Segment{{
			Index: 0, TrackID: tr.TrackID, StartDTS: tr.Keyframes[0].DTS,
			EndDTS: tr.Keyframes[0].DTS + tr.Duration, First: 0, Count: tr.Samples,
			Keyframe: 0, Bytes: tr.Bytes,
		}}
		return plan, tr.TrackID, nil
	}

	startTrack := tr.Keyframes[0].DTS
	endMovie, err := Rescale(tr.Duration, tr.Timescale, timescale)
	if err != nil {
		return nil, 0, err
	}
	windows := int((endMovie + duration - 1) / duration)
	if windows < 1 {
		windows = 1
	}
	plan.Windows = windows

	// firstKF[w] is the index of the first keyframe whose window is w.
	firstKF := make([]int, windows)
	for i := range firstKF {
		firstKF[i] = -1
	}
	for ki, kf := range tr.Keyframes {
		off, err := Rescale(kf.DTS-startTrack, tr.Timescale, timescale)
		if err != nil {
			return nil, 0, err
		}
		w := int(off / duration)
		if w >= windows {
			w = windows - 1
		}
		if firstKF[w] < 0 {
			firstKF[w] = ki
		}
	}
	plan.Misses = 0
	for w := 0; w < windows; w++ {
		if firstKF[w] < 0 {
			plan.Misses++
		}
	}
	if plan.Misses > 0 {
		plan.Aligned = false
	}

	// Build segments from the keyframes that were chosen for windows. A
	// missed window extends the previous segment; a leading missed window
	// folds into the first real segment.
	var chosenKF []int
	for w := 0; w < windows; w++ {
		if firstKF[w] >= 0 {
			chosenKF = append(chosenKF, firstKF[w])
		}
	}
	for si, ki := range chosenKF {
		first := tr.Keyframes[ki].Sample
		next := tr.Samples
		if si+1 < len(chosenKF) {
			next = tr.Keyframes[chosenKF[si+1]].Sample
		}
		var bytes int64
		for i := first; i < next && i < len(tr.Sizes); i++ {
			bytes += tr.Sizes[i]
		}
		seg := Segment{
			Index: si, TrackID: tr.TrackID, StartDTS: tr.Keyframes[ki].DTS,
			First: first, Count: next - first, Keyframe: ki, Bytes: bytes,
		}
		if si+1 < len(chosenKF) {
			seg.EndDTS = tr.Keyframes[chosenKF[si+1]].DTS
		} else {
			seg.EndDTS = startTrack + tr.Duration
		}
		plan.Segments = append(plan.Segments, seg)
	}
	return plan, tr.TrackID, nil
}

func keyframeHitRate(plan *SegmentPlan) float64 {
	if plan.Windows == 0 {
		return 0
	}
	hit := plan.Windows - plan.Misses
	if hit < 0 {
		hit = 0
	}
	return float64(hit) / float64(plan.Windows)
}

// CanonicalJSON renders the index deterministically. Tracks are sorted by
// source ID so repeated scans of identical input produce identical bytes.
func (idx *Index) CanonicalJSON() ([]byte, error) {
	sorted := *idx
	sorted.Tracks = append([]TrackIndex(nil), idx.Tracks...)
	sort.Slice(sorted.Tracks, func(i, j int) bool { return sorted.Tracks[i].TrackID < sorted.Tracks[j].TrackID })
	return json.MarshalIndent(&sorted, "", "  ")
}

// Summary is a one-line human-readable index summary.
func (idx *Index) Summary() string {
	return fmt.Sprintf("index: %d tracks, %d segments, windows=%d misses=%d keyframe_hit_rate=%.3f digest=%s",
		len(idx.Tracks), len(idx.Plan.Segments), idx.Plan.Windows, idx.Plan.Misses,
		idx.KeyframeHitRate, shortDigest(idx.Digest))
}

func shortDigest(d InputDigest) string {
	if len(d.Content) <= 12 {
		return d.Content
	}
	return d.Content[:12]
}
