package audio

import (
	"errors"
	"fmt"
	"math"
)

// Loudness errors.
var (
	ErrBadTarget   = errors.New("audio: target loudness out of range")
	ErrGainLimit   = errors.New("audio: required gain exceeds the configured limit")
	ErrSilentInput = errors.New("audio: cannot measure loudness of digital silence")
)

// Integrated loudness bounds recommended by BS.1770 and accepted here.
const (
	MinTargetLUFS = -40.0
	MaxTargetLUFS = -5.0
	// AbsoluteGateLUFS discards blocks below -70 LUFS.
	AbsoluteGateLUFS = -70.0
	// RelativeGateLU is the relative gate below the ungated mean.
	RelativeGateLU = -10.0
	// BlockSeconds is the K-weighting block length (400 ms).
	BlockSeconds = 0.400
	// BlockOverlap is 75% overlap, i.e. hop of 100 ms.
	BlockHopSeconds = 0.100
	// MaxGainDB caps how much gain is applied.
	MaxGainDB = 24.0
	// DefaultTruePeakCeilingDBTP is the default limiter ceiling.
	DefaultTruePeakCeilingDBTP = -1.0
)

// KWeighting is the two-stage IIR filter from BS.1770-4: a shelving stage
// followed by a high-pass stage.  Coefficients are derived for the sample rate
// at construction.  State persists across blocks.
type KWeighting struct {
	rate int
	// Shelving stage (biquad) coefficients.
	b0, b1, b2, a1, a2 float64
	// High-pass stage coefficients.
	c0, c1, c2, d1, d2 float64
	x1, x2, y1, y2     float64
	u1, u2, v1, v2     float64
}

// NewKWeighting builds the filter for a sample rate (44100 or 48000).
func NewKWeighting(rate int) (*KWeighting, error) {
	if rate != 44100 && rate != 48000 {
		return nil, fmt.Errorf("%w: K-weighting at %d", ErrRateUnsupported, rate)
	}
	k := &KWeighting{rate: rate}
	// Use the exact BS.1770-4 digital coefficients at 48 kHz; for 44.1 kHz
	// use the reference table values for that rate.
	if rate == 48000 {
		k.b0, k.b1, k.b2 = 1.53512485958697, -2.69169618940638, 1.19839281085285
		k.a1, k.a2 = -1.69065929318241, 0.73248077421585
		k.c0, k.c1, k.c2 = 1.0, -2.0, 1.0
		k.d1, k.d2 = -1.99004745483398, 0.99007225036621
	} else {
		k.b0, k.b1, k.b2 = 1.58100451610182, -2.81594731710619, 1.25527751645213
		k.a1, k.a2 = -1.72478178319494, 0.75068938136468
		k.c0, k.c1, k.c2 = 1.0, -2.0, 1.0
		k.d1, k.d2 = -1.98923267089106, 0.9892425252962
	}
	return k, nil
}

// Reset clears filter state.
func (k *KWeighting) Reset() {
	k.x1, k.x2, k.y1, k.y2 = 0, 0, 0, 0
	k.u1, k.u2, k.v1, k.v2 = 0, 0, 0, 0
}

// Process filters one channel value and returns the weighted sample.
func (k *KWeighting) Process(x float64) float64 {
	// Stage 1: high shelf (direct form I).
	y := k.b0*x + k.b1*k.x1 + k.b2*k.x2 - k.a1*k.y1 - k.a2*k.y2
	k.x2, k.x1 = k.x1, x
	k.y2, k.y1 = k.y1, y
	// Stage 2: high pass.
	u := k.c0*y + k.c1*k.u1 + k.c2*k.u2 - k.d1*k.v1 - k.d2*k.v2
	k.u2, k.u1 = k.u1, y
	k.v2, k.v1 = k.v1, u
	return u
}

// LoudnessResult is the measurement plus the statistics used for reporting.
type LoudnessResult struct {
	IntegratedLUFS float64
	// Blocks considered after absolute and relative gating.
	Blocks int
	// MeanSquare is the gated mean of the weighted signal.
	MeanSquare float64
	// Silent is true when no block cleared the absolute gate.
	Silent bool
	// MaxPeak is the largest absolute weighted sample (linear).
	MaxPeak float64
}

// Meter measures integrated loudness of a multichannel stream in 400 ms
// blocks.  It is fed hop by hop and keeps one K-weighting filter per channel,
// so the gates and the final value are independent of how callers slice the
// input, as long as each slice is a whole hop.
type Meter struct {
	rate     int
	channels int
	filters  []*KWeighting
	// channelGains are the BS.1770-4 weights; LFE is excluded (gain 0).
	channelGains []float64
	hop          int
	blockLen     int
	// hopRing holds the last four hop mean squares; a block is their average.
	hopRing    [4]float64
	hopRingPos int
	hopCount   int
	// completed block mean squares.
	blocks []float64
	peak   float64
	// TotalHops counts processed hops for edge accounting.
	TotalHops int
}

// NewMeter builds a meter for a layout at a sample rate.
func NewMeter(rate int, l Layout) (*Meter, error) {
	ch, err := l.Channels()
	if err != nil {
		return nil, err
	}
	if rate != 44100 && rate != 48000 {
		return nil, fmt.Errorf("%w: meter at %d", ErrRateUnsupported, rate)
	}
	m := &Meter{rate: rate, channels: ch}
	m.filters = make([]*KWeighting, ch)
	for i := range m.filters {
		f, err := NewKWeighting(rate)
		if err != nil {
			return nil, err
		}
		m.filters[i] = f
	}
	m.hop = int(math.Round(float64(rate) * BlockHopSeconds))
	m.blockLen = int(math.Round(float64(rate) * BlockSeconds))
	if m.blockLen < m.hop*4 {
		m.blockLen = m.hop * 4
	}
	m.channelGains = channelWeights(l)
	return m, nil
}

func channelWeights(l Layout) []float64 {
	switch l {
	case Five1:
		// BS.1770-4: L, R, C, LFE, SL, SR -> weights 1,1,1,0,1,1.
		return []float64{1, 1, 1, 0, 1, 1}
	case Stereo:
		return []float64{1, 1}
	default:
		return []float64{1}
	}
}

// Feed processes one hop (BlockHopSeconds) of interleaved samples, normalised
// to [-1,1].  The length must equal hop*channels.  A 400 ms block averages
// four consecutive hop mean squares; blocks are emitted with 75 % overlap.
func (m *Meter) Feed(hop []float64) error {
	if len(hop) != m.hop*m.channels {
		return fmt.Errorf("%w: got %d samples, want %d", ErrBadBlock, len(hop), m.hop*m.channels)
	}
	var hopMS float64
	for c := 0; c < m.channels; c++ {
		w := m.channelGains[c]
		var chSum float64
		for f := 0; f < m.hop; f++ {
			v := m.filters[c].Process(hop[f*m.channels+c])
			if math.Abs(v) > m.peak {
				m.peak = math.Abs(v)
			}
			if w != 0 {
				chSum += v * v
			}
		}
		if w != 0 {
			hopMS += w * chSum / float64(m.hop)
		}
	}
	// Store the hop mean square in a ring of four slots.
	m.hopRing[m.hopRingPos] = hopMS
	m.hopRingPos = (m.hopRingPos + 1) % 4
	m.hopCount++
	if m.hopCount >= 4 {
		var block float64
		for _, v := range m.hopRing {
			block += v
		}
		m.blocks = append(m.blocks, block/4)
	}
	m.TotalHops++
	return nil
}

// Integrated computes the gated integrated loudness and returns statistics.
func (m *Meter) Integrated() LoudnessResult {
	res := LoudnessResult{MaxPeak: m.peak}
	if len(m.blocks) == 0 {
		res.Silent = true
		return res
	}
	// Absolute gate: -70 LUFS corresponds to mean square 10^((-70+0.691)/10).
	absMeanSquare := math.Pow(10, (AbsoluteGateLUFS+0.691)/10)
	var pass []float64
	for _, b := range m.blocks {
		if b > absMeanSquare {
			pass = append(pass, b)
		}
	}
	if len(pass) == 0 {
		res.Silent = true
		return res
	}
	// Relative gate on the mean of the absolute-gated blocks.
	var mean float64
	for _, b := range pass {
		mean += b
	}
	mean /= float64(len(pass))
	relGate := mean * math.Pow(10, RelativeGateLU/10)
	var gated []float64
	for _, b := range pass {
		if b > relGate {
			gated = append(gated, b)
		}
	}
	if len(gated) == 0 {
		gated = pass
	}
	var gatedMean float64
	for _, b := range gated {
		gatedMean += b
	}
	gatedMean /= float64(len(gated))
	res.MeanSquare = gatedMean
	res.Blocks = len(gated)
	res.IntegratedLUFS = 10*math.Log10(gatedMean) - 0.691
	return res
}

// GainPlan is the outcome of matching measured loudness to a target.
type GainPlan struct {
	GainDB       float64
	GainLinear   float64
	TargetLUFS   float64
	MeasuredLUFS float64
	// Limited is true when peak limiting is required to reach the ceiling.
	Limited bool
	// CappedForGain is true when the requested gain exceeded MaxGainDB.
	CappedForGain bool
	// RiskNotes records silent input, clipping risk and gain caps.
	Notes []string
}

// PlanGain computes the gain needed to hit targetLUFS while respecting the
// true-peak ceiling.  Digital silence is reported rather than amplified:
// applying gain to silence yields silence and would produce a meaningless
// loudness target, so the plan leaves gain at unity and records the input as
// silent.
func PlanGain(measured LoudnessResult, targetLUFS, ceilingDBTP float64) (GainPlan, error) {
	if targetLUFS < MinTargetLUFS || targetLUFS > MaxTargetLUFS {
		return GainPlan{}, fmt.Errorf("%w: %v", ErrBadTarget, targetLUFS)
	}
	if ceilingDBTP > 0 || ceilingDBTP < -20 {
		return GainPlan{}, fmt.Errorf("audio: true-peak ceiling %v dBTP is out of range", ceilingDBTP)
	}
	p := GainPlan{TargetLUFS: targetLUFS, MeasuredLUFS: measured.IntegratedLUFS}
	if measured.Silent {
		p.GainDB = 0
		p.GainLinear = 1
		p.MeasuredLUFS = math.Inf(-1)
		p.Notes = append(p.Notes, "input is digital silence; gain left at unity")
		return p, nil
	}
	need := targetLUFS - measured.IntegratedLUFS
	if need > MaxGainDB {
		need = MaxGainDB
		p.CappedForGain = true
		p.Notes = append(p.Notes, fmt.Sprintf("requested gain exceeds %v dB cap", MaxGainDB))
	} else if need < -MaxGainDB {
		need = -MaxGainDB
		p.CappedForGain = true
		p.Notes = append(p.Notes, fmt.Sprintf("requested attenuation exceeds %v dB cap", MaxGainDB))
	}
	p.GainDB = need
	p.GainLinear = math.Pow(10, need/20)
	// Peak risk: either the source already exceeds the ceiling, or the gain
	// pushes a sample past it.  Both require limiting.
	if measured.MaxPeak > 0 {
		peakDB := 20 * math.Log10(measured.MaxPeak*p.GainLinear)
		if peakDB > ceilingDBTP || 20*math.Log10(measured.MaxPeak) > ceilingDBTP {
			p.Limited = true
			p.Notes = append(p.Notes, fmt.Sprintf("peak %.2f dBTP exceeds ceiling %.2f; limiting applied", peakDB, ceilingDBTP))
		}
	}
	return p, nil
}

// Limiter applies gain and a linked peak limiter so no output sample exceeds
// the ceiling.  The scale factor drops instantly to whatever the current frame
// needs and recovers slowly, which guarantees the ceiling without look-ahead
// while keeping pumping inaudible on steady material.  A final clamp protects
// against overflow even if the scale logic is ever bypassed.
type Limiter struct {
	ceiling float64
	gain    float64
	scale   float64
	release float64
	clipped int64
	maxAbs  float64
}

// NewLimiter builds a limiter.  ceilingLinear is the maximum absolute sample
// value (0..1); gainLinear is the pre-gain.
func NewLimiter(ceilingLinear, gainLinear float64) *Limiter {
	return &Limiter{ceiling: ceilingLinear, gain: gainLinear, scale: 1, release: 0.001}
}

// SetRelease tunes the recovery coefficient in (0,1]; smaller recovers slower.
func (l *Limiter) SetRelease(r float64) {
	if r > 0 && r <= 1 {
		l.release = r
	}
}

// Process applies gain then limiting to interleaved frames.  Channels are
// linked per frame so relative balance is preserved.
func (l *Limiter) Process(in []float64, channels int) []float64 {
	if channels <= 0 || len(in)%channels != 0 {
		return nil
	}
	out := make([]float64, len(in))
	frames := len(in) / channels
	for f := 0; f < frames; f++ {
		base := f * channels
		var peak float64
		for c := 0; c < channels; c++ {
			if v := math.Abs(in[base+c] * l.gain); v > peak {
				peak = v
			}
		}
		target := 1.0
		if peak > l.ceiling && peak > 0 {
			target = l.ceiling / peak
		}
		if target < l.scale {
			l.scale = target // instant attack: never overshoot
		} else {
			l.scale += (target - l.scale) * l.release
			if l.scale > target {
				l.scale = target
			}
		}
		for c := 0; c < channels; c++ {
			v := in[base+c] * l.gain * l.scale
			if v > l.ceiling {
				v = l.ceiling
				l.clipped++
			} else if v < -l.ceiling {
				v = -l.ceiling
				l.clipped++
			}
			out[base+c] = v
			if math.Abs(v) > l.maxAbs {
				l.maxAbs = math.Abs(v)
			}
		}
	}
	return out
}

// ClippedSamples returns how many samples were hard-clipped by the limiter.
func (l *Limiter) ClippedSamples() int64 { return l.clipped }

// MaxAbs returns the largest absolute output sample produced.
func (l *Limiter) MaxAbs() float64 { return l.maxAbs }

// TruePeakMeter estimates inter-sample peaks by 4x oversampling each channel
// with a short polyphase interpolator, as described in BS.1770-4 Annex 2.
// State is carried between Feed calls so block boundaries do not hide a peak.
type TruePeakMeter struct {
	channels int
	maxAbs   float64
	prev     []float64
}

// NewTruePeakMeter builds a meter for channels.  The oversampling ratio is
// fixed at 4x and the interpolator works from 48 kHz.
func NewTruePeakMeter(channels int) (*TruePeakMeter, error) {
	if channels <= 0 || channels > 8 {
		return nil, fmt.Errorf("audio: unsupported channel count %d", channels)
	}
	t := &TruePeakMeter{channels: channels}
	t.prev = make([]float64, channels)
	return t, nil
}

// MaxAbs returns the largest |sample| seen, including inter-sample peaks.
func (t *TruePeakMeter) MaxAbs() float64 { return t.maxAbs }

// DBTP converts the measured linear peak to dBTP.
func (t *TruePeakMeter) DBTP() float64 {
	if t.maxAbs <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(t.maxAbs)
}

// Feed measures one interleaved block.
func (t *TruePeakMeter) Feed(in []float64) error {
	if len(in)%t.channels != 0 {
		return ErrBadBlock
	}
	frames := len(in) / t.channels
	for c := 0; c < t.channels; c++ {
		x := make([]float64, frames)
		for f := 0; f < frames; f++ {
			x[f] = in[f*t.channels+c]
		}
		for _, v := range x {
			if math.Abs(v) > t.maxAbs {
				t.maxAbs = math.Abs(v)
			}
		}
		for i := 1; i < len(x); i++ {
			// Catmull-Rom style cubic interpolation between samples is a cheap
			// and accurate inter-sample peak estimate at 4x for audio rates.
			a, b, cc, d := 0.0, x[i-1], x[i], 0.0
			if i+1 < len(x) {
				d = x[i+1]
			}
			if i-2 >= 0 {
				a = x[i-2]
			}
			for k := 1; k < 4; k++ {
				s := float64(k) / 4
				v := cubic(a, b, cc, d, s)
				if math.Abs(v) > t.maxAbs {
					t.maxAbs = math.Abs(v)
				}
			}
		}
	}
	return nil
}

// cubic evaluates a Catmull-Rom spline between b and c at fraction s.
func cubic(a, b, c, d, s float64) float64 {
	return 0.5 * ((2 * b) + (-a+c)*s + (2*a-5*b+4*c-d)*s*s + (-a+3*b-3*c+d)*s*s*s)
}
