// Package sync estimates the audio/video presentation offset across edge
// nodes whose clocks drift. RTCP Sender Reports map each stream's RTP
// clock onto wall time and expose each node's clock bias relative to the
// regional reference clock. Samples from the two streams are paired by
// arrival (the only cross-stream ordering signal the receiver has), which
// makes an intrinsic clock skew visible as a difference of mapped
// presentation times. Without reports the estimator compares raw RTP
// media times, clamps the result to a bound, and flags low confidence.
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

// maxPairMs rejects a pairing whose arrivals are further apart than
// this; unrelated samples must not manufacture an offset.
const maxPairMs int64 = 60

type anchor struct {
	ntpMs     int64
	rtpTs     uint32
	arrivalMs int64
}

type sample struct {
	arrivalMs int64
	rtpTs     uint32
	eventMs   int64 // RTP media time on the reference clock
}

type stream struct {
	rate    clock.Rate
	anchors []anchor
	samples []sample
}

// Estimator accumulates Sender Reports and media samples per stream.
type Estimator struct {
	video *stream
	audio *stream
	// Malformed counts unparseable packets whose fixed RTP header still
	// identified the stream; they are never samples but remain visible in
	// the sync statistics.
	Malformed int
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

// OnSenderReport records an NTP↔RTP mapping. Reports accumulate so a
// drifting sender clock is tracked by the report nearest in RTP time.
// arrivalMs is when the report surfaced on the reference clock.
func (e *Estimator) OnSenderReport(m model.MediaType, ntpMs int64, rtpTs uint32, arrivalMs int64) {
	if m != model.MediaVideo && m != model.MediaAudio {
		return
	}
	s := e.pick(m)
	s.anchors = append(s.anchors, anchor{ntpMs: ntpMs, rtpTs: rtpTs, arrivalMs: arrivalMs})
}

// OnSample records one media event for pairing.
func (e *Estimator) OnSample(ev *model.Event) {
	if ev.Media != model.MediaVideo && ev.Media != model.MediaAudio {
		return
	}
	s := e.pick(ev.Media)
	s.samples = append(s.samples, sample{
		arrivalMs: ev.ArrivalMs, rtpTs: ev.RTPTs, eventMs: ev.EventMs,
	})
}

// OnMalformed records a corrupt packet attributable to a stream.
func (e *Estimator) OnMalformed(media model.MediaType) {
	if media == model.MediaVideo || media == model.MediaAudio {
		e.Malformed++
	}
}

// Result is the sync estimate over all paired samples.
type Result struct {
	OffsetMs     int64   // estimated audio-minus-video presentation offset
	Confidence   string  // "high" both SR, "medium" one SR, else "low"
	Paired       int     // number of audio/video pairs considered
	ClockBiasMs  int64   // audio-node bias minus video-node bias
	DriftPPM     int64   // sender clock drift estimated from consecutive SRs
	Offsets      []int64 // per-pair instantaneous offsets
	OutOfTolSegs int     // contiguous pair runs beyond ToleranceMs
}

// Estimate maps both streams onto wall time and compares presentations
// of samples that surfaced together. Pairing operates on arrival-sorted
// copies so emission reordering cannot change it.
func (e *Estimator) Estimate() Result {
	res := Result{Confidence: confidence(e.video, e.audio)}
	res.ClockBiasMs = latestBias(e.audio) - latestBias(e.video)
	res.DriftPPM = streamDriftPPM(e.audio)
	v := e.mapped(e.video)
	a := e.mapped(e.audio)
	if len(v) == 0 || len(a) == 0 {
		return res
	}
	sort.SliceStable(v, func(i, k int) bool { return v[i].arrivalMs < v[k].arrivalMs })
	sort.SliceStable(a, func(i, k int) bool { return a[i].arrivalMs < a[k].arrivalMs })
	j := 0
	for _, ve := range v {
		for j+1 < len(a) && abs64(a[j+1].arrivalMs-ve.arrivalMs) <= abs64(a[j].arrivalMs-ve.arrivalMs) {
			j++
		}
		best := j
		if j+1 < len(a) && abs64(a[j+1].arrivalMs-ve.arrivalMs) < abs64(a[j].arrivalMs-ve.arrivalMs) {
			best = j + 1
		}
		if abs64(a[best].arrivalMs-ve.arrivalMs) > maxPairMs {
			continue
		}
		d := clamp(a[best].presMs-ve.presMs, boundMs)
		res.Offsets = append(res.Offsets, d)
	}
	res.Paired = len(res.Offsets)
	if res.Paired == 0 {
		return res
	}
	res.OffsetMs = median(res.Offsets)
	inSeg := false
	for _, d := range res.Offsets {
		bad := abs64(d) > ToleranceMs
		if bad && !inSeg {
			res.OutOfTolSegs++
		}
		inSeg = bad
	}
	// Remove the per-stream node clock bias. It shifts both presentations
	// by a constant and is therefore not part of A/V sync; reporting it
	// separately lets the operator correct it at the source.
	res.OffsetMs -= res.ClockBiasMs
	if res.OffsetMs > boundMs {
		res.OffsetMs = boundMs
	} else if res.OffsetMs < -boundMs {
		res.OffsetMs = -boundMs
	}
	return res
}

// ErrAbs returns the magnitude of the offset, reported as sync error.
func (r Result) ErrAbs() int64 { return abs64(r.OffsetMs) }

type mappedSample struct {
	arrivalMs int64
	presMs    int64
}

// mapped converts each sample to a presentation time on the reference
// clock. With an SR the RTP delta from the report is added to the report's
// NTP time; a skewed sender clock therefore shifts its whole stream, which
// Estimate() removes as a node bias before reporting A/V skew. Without an
// SR the raw RTP media time is used, which is comparable only because both
// streams express it in milliseconds on the same reference clock.
func (e *Estimator) mapped(s *stream) []mappedSample {
	out := make([]mappedSample, len(s.samples))
	for i, sm := range s.samples {
		pres := sm.eventMs
		if a, ok := nearestAnchor(s.anchors, sm.rtpTs); ok {
			pres = a.ntpMs + clock.RTPMillis(clock.RTPDelta(sm.rtpTs, a.rtpTs), s.rate)
		}
		out[i] = mappedSample{arrivalMs: sm.arrivalMs, presMs: clamp(pres, boundMs*1000)}
	}
	return out
}

// nearestAnchor selects the report that precedes the sample in RTP time,
// using wrap-aware deltas; it falls back to the earliest report.
func nearestAnchor(anchors []anchor, ts uint32) (anchor, bool) {
	if len(anchors) == 0 {
		return anchor{}, false
	}
	best := anchors[0]
	for _, a := range anchors[1:] {
		if clock.RTPDelta(ts, a.rtpTs) >= 0 &&
			clock.RTPDelta(a.rtpTs, best.rtpTs) > 0 {
			best = a
		}
	}
	return best, true
}

func confidence(v, a *stream) string {
	switch {
	case len(v.anchors) > 0 && len(a.anchors) > 0:
		return "high"
	case len(v.anchors) > 0 || len(a.anchors) > 0:
		return "medium"
	default:
		return "low"
	}
}

// latestBias estimates the most recent node clock bias (sender NTP minus
// local arrival) for diagnostics.
func latestBias(s *stream) int64 {
	if len(s.anchors) == 0 {
		return 0
	}
	a := s.anchors[len(s.anchors)-1]
	return a.ntpMs - a.arrivalMs
}

// streamDriftPPM estimates clock drift in parts per million from the first
// and last Sender Reports: bias measured against local arrival time should
// grow linearly when the sender clock drifts.
func streamDriftPPM(s *stream) int64 {
	if len(s.anchors) < 2 {
		return 0
	}
	first, last := s.anchors[0], s.anchors[len(s.anchors)-1]
	span := last.arrivalMs - first.arrivalMs
	if span <= 0 {
		return 0
	}
	b0 := first.ntpMs - first.arrivalMs
	b1 := last.ntpMs - last.arrivalMs
	return (b1 - b0) * 1_000_000 / span
}

func median(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	sorted := append([]int64(nil), v...)
	sort.Slice(sorted, func(i, k int) bool { return sorted[i] < sorted[k] })
	return sorted[len(sorted)/2]
}

func clamp(v, limit int64) int64 {
	if v > limit {
		return limit
	}
	if v < -limit {
		return -limit
	}
	return v
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
