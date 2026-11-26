package event

import (
	"fmt"

	"replayproof/internal/media"
	"replayproof/internal/parse"
)

// Window is the half-open slice interval for one event.
//
// Start is the requested boundary (event minus pre-roll). VideoStart and
// AudioStart are the boundaries after alignment to a decodable video keyframe
// and an audio packet. Deviations are reported separately for each track.
type Window struct {
	Event      Event
	Start      media.Rational
	End        media.Rational
	VideoStart media.Rational
	AudioStart media.Rational
	// VideoDeviation is |VideoStart - Start|; AudioDeviation likewise.
	VideoDeviation media.Rational
	AudioDeviation media.Rational
	// VideoSample is the 1-based sample number of the aligned keyframe.
	VideoSample int
	// AudioSample is the 1-based sample number of the aligned audio packet.
	AudioSample int
}

// MaxDeviation is the largest alignment error any window may contain.
type Limits struct {
	MaxDeviation media.Rational
}

// BuildOptions configures window construction.
type BuildOptions struct {
	PreRoll  media.Rational
	PostRoll media.Rational
	Limits   Limits
}

// BuildWindows maps events onto sample indices of the repaired tracks.
//
// The interval is [event-pre, event+post). Its start is aligned backwards to
// the nearest decodable video keyframe at or before the requested boundary,
// which makes every clip independently decodable. The audio boundary is
// aligned to the audio packet containing the requested start. A boundary
// outside the track range is an error carrying the track and time range.
func BuildWindows(events []Event, video, audio *parse.Track, opts BuildOptions) ([]Window, error) {
	if video == nil {
		return nil, media.New(media.CodeNoTrack, "window.video", -1, "no video track")
	}
	out := make([]Window, 0, len(events))
	for _, ev := range events {
		start := ev.At.Sub(opts.PreRoll)
		end := ev.At.Add(opts.PostRoll)
		if !start.Less(end) {
			return nil, &media.Error{
				Code: media.CodeEventWindow, Op: "window.range",
				HasTime: true, From: start, To: end,
				Msg: "empty window",
			}
		}
		ws := Window{Event: ev, Start: start, End: end}

		v, err := alignVideo(video, start, opts.Limits)
		if err != nil {
			return nil, err
		}
		ws.VideoStart, ws.VideoSample = v.at, v.sample.Number
		ws.VideoDeviation = v.at.Sub(start).Abs()

		if audio != nil {
			a, err := alignAudio(audio, start, opts.Limits)
			if err != nil {
				return nil, err
			}
			ws.AudioStart, ws.AudioSample = a.at, a.sample.Number
			ws.AudioDeviation = a.at.Sub(start).Abs()
		}
		if err := checkLimit(ws.VideoDeviation, opts.Limits, video.ID, start); err != nil {
			return nil, err
		}
		if audio != nil {
			if err := checkLimit(ws.AudioDeviation, opts.Limits, audio.ID, start); err != nil {
				return nil, err
			}
		}
		out = append(out, ws)
	}
	return out, nil
}

type alignment struct {
	at     media.Rational
	sample parse.Sample
}

// alignVideo finds the latest keyframe whose DTS is at or before target. When
// no such keyframe exists the first keyframe is used and the deviation
// records the difference. Choosing a later keyframe would drop video that
// the caller asked for; choosing an earlier one is safe because the clip is
// still decodable from its first frame.
func alignVideo(track *parse.Track, target media.Rational, limits Limits) (alignment, error) {
	if len(track.Samples) == 0 {
		return alignment{}, &media.Error{Code: media.CodeNoKeyframe, Op: "window.video", TrackID: track.ID, Offset: -1, Msg: "track has no samples"}
	}
	best := -1
	for i := range track.Samples {
		s := track.Samples[i]
		if !s.Keyframe {
			continue
		}
		if s.DTS.LessEq(target) {
			best = i
			continue
		}
		break
	}
	if best < 0 {
		for i := range track.Samples {
			if track.Samples[i].Keyframe {
				best = i
				break
			}
		}
	}
	if best < 0 {
		return alignment{}, &media.Error{Code: media.CodeNoKeyframe, Op: "window.video", TrackID: track.ID, Offset: -1, Msg: "no keyframe in track"}
	}
	s := track.Samples[best]
	return alignment{at: s.DTS, sample: s}, nil
}

// alignAudio finds the packet whose interval contains target, or the first
// packet when target precedes the track. Audio packets are independently
// decodable so no keyframe scan is needed.
func alignAudio(track *parse.Track, target media.Rational, limits Limits) (alignment, error) {
	if len(track.Samples) == 0 {
		return alignment{}, &media.Error{Code: media.CodeNoKeyframe, Op: "window.audio", TrackID: track.ID, Offset: -1, Msg: "track has no samples"}
	}
	best := 0
	for i := range track.Samples {
		if track.Samples[i].DTS.LessEq(target) {
			best = i
			continue
		}
		break
	}
	s := track.Samples[best]
	return alignment{at: s.DTS, sample: s}, nil
}

func checkLimit(dev media.Rational, limits Limits, trackID uint32, start media.Rational) error {
	if limits.MaxDeviation.Sign() <= 0 {
		return nil
	}
	if dev.Greater(limits.MaxDeviation) {
		return &media.Error{
			Code: media.CodeEventWindow, Op: "window.deviation",
			TrackID: trackID, HasTime: true, From: start, To: start,
			Msg: fmt.Sprintf("alignment error %d us exceeds limit %d us", dev.Micros(), limits.MaxDeviation.Micros()),
		}
	}
	return nil
}

// SliceRange is the sample extent of one track within a window.
type SliceRange struct {
	TrackID uint32
	First   int // 0-based inclusive
	Last    int // 0-based exclusive
}

// ClipPlan is the full sample extent of a clipped event.
type ClipPlan struct {
	Window Window
	Video  SliceRange
	Audio  SliceRange
	// End is the actual clip end after decoding constraints are applied.
	End media.Rational
}

// PlanClip resolves window boundaries to sample ranges. The clip ends at the
// last video sample that starts before the requested end, and audio is cut at
// the same media time so the tracks stay duration-matched.
func PlanClip(win Window, video, audio *parse.Track) (ClipPlan, error) {
	plan := ClipPlan{Window: win}
	if video == nil {
		return plan, media.New(media.CodeNoTrack, "window.plan", -1, "no video track")
	}
	vFirst := win.VideoSample - 1
	if vFirst < 0 || vFirst >= len(video.Samples) {
		return plan, media.New(media.CodeInvalidBox, "window.plan", -1, "video start out of range")
	}
	vLast := vFirst
	for i := vFirst; i < len(video.Samples); i++ {
		if video.Samples[i].DTS.Less(win.End) {
			vLast = i + 1
			continue
		}
		break
	}
	if vLast <= vFirst {
		vLast = vFirst + 1
	}
	plan.Video = SliceRange{TrackID: video.ID, First: vFirst, Last: vLast}
	plan.End = video.Samples[vLast-1].End()
	if audio != nil {
		aFirst := win.AudioSample - 1
		if aFirst < 0 || aFirst >= len(audio.Samples) {
			return plan, media.New(media.CodeInvalidBox, "window.plan", -1, "audio start out of range")
		}
		aLast := aFirst
		for i := aFirst; i < len(audio.Samples); i++ {
			if audio.Samples[i].DTS.Less(plan.End) {
				aLast = i + 1
				continue
			}
			break
		}
		if aLast <= aFirst {
			aLast = aFirst + 1
		}
		plan.Audio = SliceRange{TrackID: audio.ID, First: aFirst, Last: aLast}
	}
	return plan, nil
}
