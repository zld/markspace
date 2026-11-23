// Package sync estimates the audio/video presentation offset across
// edge nodes whose clocks drift. With RTCP Sender Reports it maps each
// stream's RTP clock onto wall time; without them it falls back to a
// bounded arrival-based estimate flagged with low confidence.
package sync

import (
	"sort"

	"mediad/internal/clock"
	"mediad/internal/model"
)

// ToleranceMs is the maximum acceptable A/V offset magnitude.
const ToleranceMs int64 = 40

// boundMs caps the fallback estimate when no Sender Report exists.
const boundMs int64 = 500

type stream struct {
	rate      clock.Rate
	hasSR     bool
	srNTPMs   int64
	srRTP     uint32
	firstEvMs int64
	firstArr  int64
	seen      bool
	// wallMs[i] is the estimated wall-clock time of sample i, paired
	// with eventMs[i] for cross-stream matching.
	eventMs []int64
	wallMs  []int64
}

// Estimator accumulates Sender Reports and media events per SSRC.
type Estimator struct {
	video *stream
	audio *stream
}

// NewEstimator creates an estimator for the declared clock rates.
func NewEstimator() *Estimator {
	return &Estimator{
		video: &stream{rate: clock.RTPClockVideo},
		audio: &stream{rate: clock.RTPClockAudio},
	}
}

func (e *Estimator) pick(m model.MediaType) *stream {
	if m == model.MediaAudio {
		return e.audio
	}
	return e.video
}

// OnSenderReport records the NTP↔RTP mapping for a stream.
func (e *Estimator) OnSenderReport(m model.MediaType, ntpMs int64, rtpTs uint32) {
	s := e.pick(m)
	s.hasSR = true
	s.srNTPMs = ntpMs
	s.srRTP = rtpTs
}

// OnSample maps one media event onto the estimated wall clock.
func (e *Estimator) OnSample(ev *model.Event) {
	s := e.pick(ev.Media)
	if !s.seen {
		s.seen = true
		s.firstEvMs = ev.EventMs
		s.firstArr = ev.ArrivalMs
	}
	var wall int64
	if s.hasSR {
		wall = s.srNTPMs + clock.RTPMillis(clock.RTPDelta(ev.RTPTs, s.srRTP), s.rate)
	} else {
		// Bounded fallback: anchor the RTP timeline to the first
		// arrival. Drift is unobservable, so cap the implied offset.
		wall = ev.EventMs + (s.firstArr - s.firstEvMs)
		if wall > boundMs*1000 {
			wall = boundMs * 1000
		}
	}
	s.eventMs = append(s.eventMs, ev.EventMs)
	s.wallMs = append(s.wallMs, wall)
}

// Result is the sync estimate over all paired samples.
type Result struct {
	OffsetMs     int64   // estimated audio-minus-video offset
	Confidence   string  // "high" with Sender Reports, else "low"
	Offsets      []int64 // per-pair instantaneous offsets
	OutOfTolSegs int     // contiguous segments beyond ToleranceMs
}

// Estimate pairs audio and video samples by nearest presentation time
// and returns the median instantaneous offset.
func (e *Estimator) Estimate() Result {
	res := Result{Confidence: "high"}
	if !e.video.hasSR || !e.audio.hasSR {
		res.Confidence = "low"
	}
	a, v := e.audio, e.video
	if len(a.eventMs) == 0 || len(v.eventMs) == 0 {
		return res
	}
	var offsets []int64
	j := 0
	for i, evMs := range v.eventMs {
		for j+1 < len(a.eventMs) && a.eventMs[j+1] <= evMs {
			j++
		}
		best := j
		if j+1 < len(a.eventMs) && a.eventMs[j+1]-evMs < evMs-a.eventMs[j] {
			best = j + 1
		}
		d := a.wallMs[best] - v.wallMs[i]
		if d > boundMs {
			d = boundMs
		} else if d < -boundMs {
			d = -boundMs
		}
		offsets = append(offsets, d)
	}
	res.Offsets = offsets
	sorted := append([]int64(nil), offsets...)
	sort.Slice(sorted, func(i, k int) bool { return sorted[i] < sorted[k] })
	res.OffsetMs = sorted[len(sorted)/2]
	// Count contiguous out-of-tolerance segments.
	inSeg := false
	for _, d := range offsets {
		bad := d > ToleranceMs || d < -ToleranceMs
		if bad && !inSeg {
			res.OutOfTolSegs++
		}
		inSeg = bad
	}
	return res
}
