// Package clip plans and writes review fragments. A plan aligns every event
// cut to a decodable keyframe inside a pre/post window, keeps the original
// event instant in the manifest, and never re-encodes samples.
package clip

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"replaypipe/internal/index"
	"replaypipe/internal/metadata"
	"replaypipe/internal/timebase"
)

// Budget is the processing envelope. Planning order is quality gate first,
// then work budget, then delivery size.
type Budget struct {
	PreNS        int64
	PostNS       int64
	MinWidth     uint16
	MinFPS       float64
	MaxWorkBytes uint64
	MaxOutBytes  uint64
}

// DefaultBudget matches the acceptance command.
func DefaultBudget() Budget {
	return Budget{
		PreNS:        5 * 1_000_000_000,
		PostNS:       8 * 1_000_000_000,
		MinWidth:     1280,
		MinFPS:       30,
		MaxWorkBytes: 64 << 20,
		MaxOutBytes:  16 << 20,
	}
}

// QualityReport records the gate and budget decisions for one representation.
type QualityReport struct {
	Width          uint16  `json:"width"`
	Height         uint16  `json:"height"`
	FPS            float64 `json:"fps"`
	MinWidth       uint16  `json:"min_width"`
	MinFPS         float64 `json:"min_fps"`
	GateOK         bool    `json:"gate_ok"`
	WorkBytes      uint64  `json:"work_bytes"`
	MaxWork        uint64  `json:"max_work_bytes"`
	BudgetOK       bool    `json:"budget_ok"`
	Reencodes      bool    `json:"reencodes"`
	Representation string  `json:"representation"`
}

// Reason explains why a representation was rejected.
type Reason string

const (
	ReasonLowWidth   Reason = "width below minimum"
	ReasonLowFPS     Reason = "frame rate below minimum"
	ReasonOverBudget Reason = "work budget exceeded"
	ReasonReencode   Reason = "would require re-encode"
)

// Candidate is one selectable rendition of the clip window.
type Candidate struct {
	Name       string
	Width      uint16
	Height     uint16
	FPS        float64
	Reencode   bool
	VideoBytes uint64
	AudioBytes uint64
}

// Slice is a planned review fragment.
type Slice struct {
	ID             string        `json:"id"`
	EventID        string        `json:"event_id"`
	EventKind      metadata.Kind `json:"event_kind"`
	EventStartNS   int64         `json:"event_start_ns"`
	EventEndNS     int64         `json:"event_end_ns"`
	WindowStartNS  int64         `json:"window_start_ns"`
	WindowEndNS    int64         `json:"window_end_ns"`
	AlignedStartNS int64         `json:"aligned_start_ns"`
	AlignedEndNS   int64         `json:"aligned_end_ns"`
	StartKeyframe  int           `json:"start_keyframe"`
	EndKeyframe    int           `json:"end_keyframe"`
	VideoStart     int           `json:"video_start_sample"`
	VideoEnd       int           `json:"video_end_sample"`
	AudioStart     int           `json:"audio_start_sample"`
	AudioEnd       int           `json:"audio_end_sample"`
	Quality        QualityReport `json:"quality"`
	WorkBytes      uint64        `json:"work_bytes"`
	DependsOn      []string      `json:"depends_on,omitempty"`
	// Reused marks slices whose asset did not change between runs.
	Reused bool `json:"reused"`
	// EventDigest binds the slice to the exact event payload.
	EventDigest string `json:"event_digest"`
}

// Plan is the complete cutting decision for a presentation.
type Plan struct {
	MediaContentDigest  string      `json:"media_content_digest"`
	MediaEnvelopeDigest string      `json:"media_envelope_digest"`
	EventDigest         string      `json:"event_digest"`
	Slices              []Slice     `json:"slices"`
	Rejected            []Rejection `json:"rejected,omitempty"`
	Truncated           bool        `json:"truncated"`
	ReusedCount         int         `json:"reused_count"`
	PlannedCount        int         `json:"planned_count"`
}

// Rejection records a representation that failed the ordered selection.
type Rejection struct {
	Candidate string `json:"candidate"`
	Reason    Reason `json:"reason"`
	Detail    string `json:"detail"`
}

// PlanOptions ties a parsed presentation to the events and budgets.
type PlanOptions struct {
	Media     *index.Media
	Keyframes *index.KeyframeIndex
	Events    []metadata.Event
	Budget    Budget
	// Candidates are renditions offered by the packaging layer. When empty
	// the single parsed video track is used.
	Candidates []Candidate
	// Known holds slice ids whose asset digest is unchanged from a previous
	// commit. Matching slices are marked Reused and not re-cut.
	Known map[string]string
}

// GetForMedia is a convenience wrapper that builds the keyframe index.
func GetForMedia(media *index.Media) (*index.KeyframeIndex, *timebase.Error) {
	video := media.VideoTrack()
	if video == nil {
		return nil, timebase.Newf(timebase.CodeQualityGate, 0, -1, "presentation has no video track")
	}
	return index.BuildKeyframeIndex(video)
}

// BuildPlan validates the quality gate once for the presentation and then
// plans one slice per event.
func BuildPlan(opts PlanOptions) (*Plan, *timebase.Error) {
	media := opts.Media
	video := media.VideoTrack()
	if video == nil {
		return nil, timebase.Fail(timebase.CodeQualityGate, 0, -1).Wrap(nil, "no video track")
	}
	kf := opts.Keyframes
	if kf == nil {
		var e *timebase.Error
		kf, e = index.BuildKeyframeIndex(video)
		if e != nil {
			return nil, e
		}
	}
	if kf.Len() == 0 {
		return nil, timebase.Newf(timebase.CodeNoKeyframe, video.ID, -1, "video track has no sync samples")
	}
	budget := opts.Budget
	if budget.MaxWorkBytes == 0 {
		budget = DefaultBudget()
	}
	cands := opts.Candidates
	if len(cands) == 0 {
		cands = []Candidate{{
			Name:       "native",
			Width:      video.Width,
			Height:     video.Height,
			FPS:        video.FrameRate(),
			Reencode:   false,
			VideoBytes: uint64(video.Duration) * 0,
		}}
	}
	selected, rejected, e := selectRepresentation(cands, budget, totalSampleBytes(media))
	if e != nil {
		return nil, e
	}
	chosen := selected[0]
	plan := &Plan{
		MediaContentDigest:  media.ContentDigest,
		MediaEnvelopeDigest: media.EnvelopeDigest,
		Slices:              []Slice{},
		Rejected:            rejected,
	}
	events := append([]metadata.Event(nil), opts.Events...)
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].StartNS != events[j].StartNS {
			return events[i].StartNS < events[j].StartNS
		}
		return events[i].ID < events[j].ID
	})
	plan.EventDigest = index.EventDigest(events)
	mediaRange := timebase.RangeNS{Start: 0, End: media.DurationNS()}
	var work uint64
	for i, ev := range events {
		slice, e := planEventSlice(i, ev, media, kf, budget, mediaRange)
		if e != nil {
			return nil, e
		}
		slice.Quality = qualityFor(chosen, budget)
		if chosen.sampleRange(media, slice) > budget.MaxWorkBytes {
			return nil, timebase.Newf(timebase.CodeBudgetExceeded, video.ID, ev.SourceOffset,
				"slice %q needs %d bytes, budget %d", slice.ID, chosen.sampleRange(media, slice), budget.MaxWorkBytes)
		}
		work += chosen.sampleRange(media, slice)
		if work > budget.MaxWorkBytes {
			return nil, timebase.Newf(timebase.CodeBudgetExceeded, video.ID, ev.SourceOffset,
				"cumulative work %d exceeds budget %d", work, budget.MaxWorkBytes)
		}
		slice.WorkBytes = chosen.sampleRange(media, slice)
		slice.DependsOn = dependsFor(slice, media)
		slice.EventDigest = ev.DigestHex()
		if digest, ok := opts.Known[slice.ID]; ok && digest == slice.AssetDigest(media) {
			slice.Reused = true
			plan.ReusedCount++
		} else {
			plan.PlannedCount++
		}
		plan.Slices = append(plan.Slices, slice)
	}
	return plan, nil
}

// selectRepresentation applies quality gate -> work budget -> smallest
// acceptable output. It always returns at least one candidate or an error.
func selectRepresentation(cands []Candidate, budget Budget, mediaBytes uint64) ([]Candidate, []Rejection, *timebase.Error) {
	var rejected []Rejection
	var passing []Candidate
	for _, c := range cands {
		switch {
		case c.Width < budget.MinWidth:
			rejected = append(rejected, Rejection{Candidate: c.Name, Reason: ReasonLowWidth, Detail: fmt.Sprintf("%d < %d", c.Width, budget.MinWidth)})
		case c.FPS+1e-9 < budget.MinFPS:
			rejected = append(rejected, Rejection{Candidate: c.Name, Reason: ReasonLowFPS, Detail: fmt.Sprintf("%.3f < %.3f", c.FPS, budget.MinFPS)})
		case c.Reencode:
			rejected = append(rejected, Rejection{Candidate: c.Name, Reason: ReasonReencode, Detail: "sample ranges must be copied as stored"})
		default:
			passing = append(passing, c)
		}
	}
	if len(passing) == 0 {
		return nil, rejected, timebase.Newf(timebase.CodeQualityGate, 0, -1,
			"no representation meets width>=%d and fps>=%.3f without re-encoding", budget.MinWidth, budget.MinFPS)
	}
	sort.SliceStable(passing, func(i, j int) bool {
		bi := passing[i].VideoBytes + passing[i].AudioBytes
		bj := passing[j].VideoBytes + passing[j].AudioBytes
		if bi != bj {
			return bi < bj
		}
		return passing[i].Name < passing[j].Name
	})
	best := passing[0]
	if best.VideoBytes+best.AudioBytes > budget.MaxWorkBytes {
		return nil, rejected, timebase.Newf(timebase.CodeBudgetExceeded, 0, -1,
			"smallest acceptable representation needs %d bytes, budget %d",
			best.VideoBytes+best.AudioBytes, budget.MaxWorkBytes)
	}
	return passing, rejected, nil
}

func (c Candidate) sampleRange(media *index.Media, s Slice) uint64 {
	var n uint64
	if v := media.VideoTrack(); v != nil {
		for i := s.VideoStart; i < s.VideoEnd && i < len(v.Samples); i++ {
			n += uint64(v.Samples[i].Size)
		}
	}
	if a := media.AudioTrack(); a != nil {
		for i := s.AudioStart; i < s.AudioEnd && i < len(a.Samples); i++ {
			n += uint64(a.Samples[i].Size)
		}
	}
	if c.VideoBytes > 0 && n == 0 {
		n = c.VideoBytes
	}
	return n
}

func qualityFor(c Candidate, b Budget) QualityReport {
	return QualityReport{
		Width: c.Width, Height: c.Height, FPS: c.FPS,
		MinWidth: b.MinWidth, MinFPS: b.MinFPS,
		GateOK:    c.Width >= b.MinWidth && c.FPS+1e-9 >= b.MinFPS && !c.Reencode,
		WorkBytes: c.VideoBytes + c.AudioBytes, MaxWork: b.MaxWorkBytes,
		BudgetOK:  c.VideoBytes+c.AudioBytes <= b.MaxWorkBytes,
		Reencodes: c.Reencode, Representation: c.Name,
	}
}

func totalSampleBytes(media *index.Media) uint64 {
	var n uint64
	for _, t := range media.Tracks {
		for _, s := range t.Samples {
			n += uint64(s.Size)
		}
	}
	return n
}

// planEventSlice aligns one event to keyframes inside the pre/post window.
// The window is always clamped to the media range. The start cut is the best
// keyframe between window start and the event start (falling back to the
// first keyframe after the event start), so the fragment can decode from its
// first frame. The end cut is the latest keyframe that still lies inside the
// window, which keeps post-roll without extending past event+post.
func planEventSlice(ord int, ev metadata.Event, media *index.Media,
	kf *index.KeyframeIndex, budget Budget, mediaRange timebase.RangeNS) (Slice, *timebase.Error) {
	video := media.VideoTrack()
	if video == nil {
		return Slice{}, timebase.Newf(timebase.CodeQualityGate, 0, ev.SourceOffset, "no video track")
	}
	startNS := ev.StartNS
	endNS := ev.EndNS
	if endNS <= startNS {
		endNS = startNS
	}
	window := timebase.RangeNS{
		Start: startNS - budget.PreNS,
		End:   endNS + budget.PostNS,
	}.Clamp(mediaRange)
	if window.Len() <= 0 {
		return Slice{}, timebase.Newf(timebase.CodeEventOutOfRange, ev.TrackID, ev.SourceOffset,
			"event %q window is empty after clamping", ev.ID)
	}
	startKF, ok := startKeyframeFor(kf, window, startNS)
	if !ok {
		return Slice{}, timebase.Newf(timebase.CodeNoKeyframe, video.ID, ev.SourceOffset,
			"no decodable keyframe in window [%d,%d) for event %q", window.Start, window.End, ev.ID)
	}
	alignedStart := startKF.PTSNS()
	// The end cut needs no keyframe (decoding simply stops), but it must
	// cover the whole event. Prefer the latest keyframe inside the window and
	// never cut before the event end or beyond the media end.
	alignedEnd := window.End
	if f, ok := kf.AtOrBefore(window.End); ok && f.PTSNS() > alignedStart {
		alignedEnd = f.PTSNS()
	}
	if alignedEnd < endNS {
		alignedEnd = endNS
	}
	if alignedEnd > mediaRange.End {
		alignedEnd = mediaRange.End
	}
	if alignedEnd <= alignedStart {
		if f, ok := kf.AtOrAfter(startNS); ok && f.PTSNS() < window.End {
			alignedEnd = f.PTSNS()
		} else if last := kf.Frames[len(kf.Frames)-1]; last.PTSNS() > alignedStart {
			alignedEnd = last.PTSNS()
		} else {
			return Slice{}, timebase.Newf(timebase.CodeNoKeyframe, video.ID, ev.SourceOffset,
				"event %q has no usable keyframe span", ev.ID)
		}
	}
	vs := sampleAtOrAfter(video, alignedStart)
	ve := sampleAtOrAfter(video, alignedEnd)
	if ve <= vs {
		ve = vs + 1
	}
	if ve > len(video.Samples) {
		ve = len(video.Samples)
	}
	as, ae := 0, 0
	if audio := media.AudioTrack(); audio != nil {
		as = sampleAtOrAfter(audio, alignedStart)
		ae = sampleAtOrAfter(audio, alignedEnd)
		if ae <= as {
			ae = as
		}
	}
	id := fmt.Sprintf("clip-%02d-%s", ord+1, ev.ID)
	return Slice{
		ID: id, EventID: ev.ID, EventKind: ev.Kind,
		EventStartNS: ev.StartNS, EventEndNS: ev.EndNS,
		WindowStartNS: window.Start, WindowEndNS: window.End,
		AlignedStartNS: alignedStart, AlignedEndNS: alignedEnd,
		StartKeyframe: startKF.Index, EndKeyframe: endKFOrdinal(kf, alignedEnd),
		VideoStart: vs, VideoEnd: ve, AudioStart: as, AudioEnd: ae,
	}, nil
}

// startKeyframeFor picks the latest keyframe at or before the event start but
// no earlier than the window start. If none exists, it takes the first
// keyframe after the event start that still lies inside the window.
func startKeyframeFor(kf *index.KeyframeIndex, window timebase.RangeNS, eventStart int64) (index.Keyframe, bool) {
	if f, ok := kf.AtOrBefore(eventStart); ok && f.PTSNS() >= window.Start {
		return f, true
	}
	if f, ok := kf.AtOrAfter(eventStart); ok && f.PTSNS() < window.End {
		return f, true
	}
	if f, ok := kf.AtOrAfter(window.Start); ok && f.PTSNS() < window.End {
		return f, true
	}
	return index.Keyframe{}, false
}

func endKFOrdinal(kf *index.KeyframeIndex, ns int64) int {
	if f, ok := kf.AtOrAfter(ns); ok {
		return f.Index
	}
	if kf.Len() > 0 {
		return kf.Frames[kf.Len()-1].Index
	}
	return -1
}

func sampleAtOrAfter(tr *index.Track, ns int64) int {
	lo, hi := 0, len(tr.Samples)
	for lo < hi {
		mid := (lo + hi) / 2
		if tr.ToNanosCached(tr.Samples[mid].DTS, tr.Samples[mid].Offset) < ns {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func dependsFor(s Slice, media *index.Media) []string {
	deps := []string{}
	if a := media.AudioTrack(); a != nil && s.AudioEnd > s.AudioStart {
		deps = append(deps, fmt.Sprintf("audio:%d", a.ID))
	}
	if v := media.VideoTrack(); v != nil {
		deps = append(deps, fmt.Sprintf("video:%d:keyframe:%d", v.ID, s.StartKeyframe))
	}
	return deps
}

// AssetDigest binds a slice to the exact samples and event that produce it.
// A change in either invalidates the asset; unrelated slices stay reusable.
func (s Slice) AssetDigest(media *index.Media) string {
	h := sha256.New()
	fmt.Fprintf(h, "ev:%s\x00win:%d:%d\x00kf:%d:%d\x00",
		s.EventDigest, s.AlignedStartNS, s.AlignedEndNS, s.StartKeyframe, s.EndKeyframe)
	if v := media.VideoTrack(); v != nil {
		fmt.Fprintf(h, "video:%d\x00", v.ID)
		for i := s.VideoStart; i < s.VideoEnd && i < len(v.Samples); i++ {
			smp := v.Samples[i]
			fmt.Fprintf(h, "%d:%d:%t:%s\x00", smp.Index, smp.Size, smp.Sync, smp.Digest)
		}
	}
	if a := media.AudioTrack(); a != nil {
		fmt.Fprintf(h, "audio:%d\x00", a.ID)
		for i := s.AudioStart; i < s.AudioEnd && i < len(a.Samples); i++ {
			smp := a.Samples[i]
			fmt.Fprintf(h, "%d:%d:%s\x00", smp.Index, smp.Size, smp.Digest)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}
