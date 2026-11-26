package media

import (
	"math/big"
	"sort"
)

// Gap describes a missing interval or explicitly lost packet range.
type Gap struct {
	TrackID       uint32 `json:"track_id"`
	AfterSequence int    `json:"after_sequence,omitempty"`
	StartNS       int64  `json:"start_ns"`
	EndNS         int64  `json:"end_ns"`
	DurationNS    int64  `json:"duration_ns"`
	MissingCount  int    `json:"missing_count,omitempty"`
	Reason        string `json:"reason"`
}

// TimelineReport contains explainable continuity and A/V quality metrics.
type TimelineReport struct {
	EventCount  int     `json:"event_count"`
	AudioCount  int     `json:"audio_count"`
	VideoCount  int     `json:"video_count"`
	CueCount    int     `json:"cue_count"`
	FirstPTSNS  int64   `json:"first_pts_ns"`
	LastPTSNS   int64   `json:"last_pts_ns"`
	EndNS       int64   `json:"end_ns"`
	SpanNS      int64   `json:"span_ns"`
	CoveredNS   int64   `json:"covered_ns"`
	CoveragePPM int     `json:"coverage_ppm"`
	MaxAVSkewNS int64   `json:"max_av_skew_ns"`
	MaxAVSkewMS float64 `json:"max_av_skew_ms"`
	LostEvents  int     `json:"lost_events"`
	Gaps        []Gap   `json:"gaps,omitempty"`
}

// AnalyzeTimeline computes coverage, gaps, and nearest-point A/V skew.
func AnalyzeTimeline(input []Event) TimelineReport {
	events := SortEvents(input)
	r := TimelineReport{EventCount: len(events)}
	if len(events) == 0 {
		return r
	}
	r.FirstPTSNS = events[0].PTSNS
	r.LastPTSNS = events[len(events)-1].PTSNS
	r.EndNS = r.LastPTSNS

	intervals := make([][2]int64, 0, len(events))
	for _, e := range events {
		switch e.Kind {
		case KindAudio:
			r.AudioCount++
		case KindVideo:
			r.VideoCount++
		case KindCue:
			r.CueCount++
		}
		if e.Lost {
			if e.MissingCount > 0 {
				r.LostEvents += e.MissingCount
			} else {
				r.LostEvents++
			}
			missing := e.MissingCount
			if missing == 0 {
				missing = 1
			}
			r.Gaps = append(r.Gaps, Gap{
				TrackID:      e.TrackID,
				StartNS:      e.PTSNS,
				EndNS:        e.EndNS,
				DurationNS:   e.DurationNS,
				MissingCount: missing,
				Reason:       e.Detail,
			})
		}
		if e.EndNS > r.EndNS {
			r.EndNS = e.EndNS
		}
		if e.DurationNS > 0 && !e.Lost && e.EndNS > e.PTSNS {
			intervals = append(intervals, [2]int64{e.PTSNS, e.EndNS})
		}
	}
	r.SpanNS = r.EndNS - r.FirstPTSNS
	r.CoveredNS = unionDuration(intervals)
	r.CoveragePPM = coveragePPM(r.CoveredNS, r.SpanNS)
	r.Gaps = append(r.Gaps, detectTrackGaps(events)...)
	r.MaxAVSkewNS = MaxAVSkew(events)
	r.MaxAVSkewMS = float64(r.MaxAVSkewNS) / 1_000_000.0
	return r
}

func unionDuration(in [][2]int64) int64 {
	if len(in) == 0 {
		return 0
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i][0] != in[j][0] {
			return in[i][0] < in[j][0]
		}
		return in[i][1] < in[j][1]
	})
	var total int64
	curL, curR := in[0][0], in[0][1]
	for _, iv := range in[1:] {
		if iv[0] <= curR {
			if iv[1] > curR {
				curR = iv[1]
			}
			continue
		}
		total += curR - curL
		curL, curR = iv[0], iv[1]
	}
	total += curR - curL
	return total
}

func coveragePPM(covered, span int64) int {
	if span <= 0 {
		if covered > 0 {
			return 1_000_000
		}
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(covered), big.NewInt(1_000_000))
	n.Quo(n, big.NewInt(span))
	if !n.IsInt64() {
		return 0
	}
	v := n.Int64()
	if v > 1_000_000 {
		v = 1_000_000
	}
	if v < 0 {
		v = 0
	}
	return int(v)
}

func detectTrackGaps(events []Event) []Gap {
	byTrack := map[uint32][]Event{}
	for _, e := range events {
		if e.Kind == KindCue || e.Lost {
			continue
		}
		byTrack[e.TrackID] = append(byTrack[e.TrackID], e)
	}
	var gaps []Gap
	for track, list := range byTrack {
		sort.SliceStable(list, func(i, j int) bool { return list[i].PTSNS < list[j].PTSNS })
		for i := 1; i < len(list); i++ {
			prev, cur := list[i-1], list[i]
			if prev.EndNS <= prev.PTSNS || cur.PTSNS <= prev.EndNS {
				continue
			}
			gaps = append(gaps, Gap{
				TrackID:       track,
				AfterSequence: prev.Sequence,
				StartNS:       prev.EndNS,
				EndNS:         cur.PTSNS,
				DurationNS:    cur.PTSNS - prev.EndNS,
				Reason:        "non_overlapping_interval",
			})
		}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].StartNS < gaps[j].StartNS })
	return gaps
}

// MaxAVSkew returns the largest nearest-audio PTS distance for each video
// frame. Audio packets may be denser than video frames, so every audio packet
// is not required to have a matching video PTS. Missing pairs are not invented.
func MaxAVSkew(events []Event) int64 {
	audio := ByKind(events, KindAudio)
	video := ByKind(events, KindVideo)
	if len(audio) == 0 || len(video) == 0 {
		return 0
	}
	var max int64
	for _, v := range video {
		if d := nearestPTSDistance(audio, v.PTSNS); d > max {
			max = d
		}
	}
	return max
}

func nearestPTSDistance(sorted []Event, pts int64) int64 {
	i := sort.Search(len(sorted), func(i int) bool { return sorted[i].PTSNS >= pts })
	best := int64(-1)
	check := func(j int) {
		if j < 0 || j >= len(sorted) {
			return
		}
		d := sorted[j].PTSNS - pts
		if d < 0 {
			d = -d
		}
		if best < 0 || d < best {
			best = d
		}
	}
	check(i)
	check(i - 1)
	if best < 0 {
		return 0
	}
	return best
}
