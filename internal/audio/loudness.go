package audio

import (
	"math"

	"mediad/internal/mederr"
)

// SilenceFloorDB is returned instead of -Inf/NaN for digital silence.
const SilenceFloorDB = -120.0

// LevelReport contains deterministic loudness and peak measurements.
type LevelReport struct {
	IntegratedLUFS float64 `json:"integrated_lufs"`
	PeakDBFS       float64 `json:"peak_dbfs"`
	PeakLinear     float64 `json:"peak_linear"`
	RMSDBFS        float64 `json:"rms_dbfs"`
	SampleCount    int64   `json:"sample_count"`
	Silent         bool    `json:"silent"`
}

// Meter is a streaming K-weighted energy and peak accumulator. It is safe
// for chunked processing: call Add for successive output chunks and Report
// once at the end. Filter state is intentionally retained across chunks.
type Meter struct {
	rate     int
	channels int
	count    int64
	energy   float64
	peak     float64
	hp       []biquad
	shelf    []biquad
}

// NewMeter creates a meter for rate Hz and channels channels.
func NewMeter(rate, channels int) (*Meter, error) {
	if rate <= 0 || channels <= 0 {
		return nil, mederr.New(mederr.CatAudio, "loudness", -1, -1, false,
			"invalid meter format rate=%d channels=%d", rate, channels)
	}
	m := &Meter{rate: rate, channels: channels}
	m.hp = make([]biquad, channels)
	m.shelf = make([]biquad, channels)
	for c := 0; c < channels; c++ {
		// BS.1770-like high-pass and high shelf. Coefficients are derived
		// from the actual rate rather than assuming 48 kHz.
		m.hp[c] = newHighPass(38.0, 0.5, rate)
		m.shelf[c] = newHighShelf(1680.0, 0.707, 4.0, rate)
	}
	return m, nil
}

// AddBuffer accumulates one planar buffer.
func (m *Meter) AddBuffer(b *Buffer) error {
	if b == nil || b.Rate != m.rate || b.Layout.Channels != m.channels {
		return mederr.New(mederr.CatAudio, "loudness", -1, -1, false,
			"meter format mismatch: buffer=%+v meter rate=%d channels=%d", b, m.rate, m.channels)
	}
	frames := b.Frames()
	for i := 0; i < frames; i++ {
		for c := 0; c < m.channels; c++ {
			x := b.Planar[c][i]
			if math.IsNaN(x) || math.IsInf(x, 0) {
				x = 0
			}
			if ax := math.Abs(x); ax > m.peak {
				m.peak = ax
			}
			y := m.hp[c].process(x)
			y = m.shelf[c].process(y)
			m.energy += y * y
			m.count++
		}
	}
	return nil
}

// Report returns the accumulated levels. Silence is reported with finite
// floor values and Silent=true; callers never observe NaN.
func (m *Meter) Report() LevelReport {
	if m == nil || m.count == 0 || m.energy <= 1e-14 {
		return LevelReport{
			IntegratedLUFS: SilenceFloorDB,
			PeakDBFS:       SilenceFloorDB,
			PeakLinear:     0,
			RMSDBFS:        SilenceFloorDB,
			SampleCount:    0,
			Silent:         true,
		}
	}
	mean := m.energy / float64(m.count)
	lufs := -0.691 + 10*math.Log10(mean)
	rmsDB := 10 * math.Log10(mean)
	peakDB := LinearToDB(m.peak)
	if peakDB < SilenceFloorDB {
		peakDB = SilenceFloorDB
	}
	return LevelReport{
		IntegratedLUFS: clampFinite(lufs),
		PeakDBFS:       peakDB,
		PeakLinear:     m.peak,
		RMSDBFS:        clampFinite(rmsDB),
		SampleCount:    m.count,
		Silent:         false,
	}
}

// Measure performs a one-shot loudness measurement.
func Measure(b *Buffer) (LevelReport, error) {
	m, err := NewMeter(b.Rate, b.Layout.Channels)
	if err != nil {
		return LevelReport{}, err
	}
	if err := m.AddBuffer(b); err != nil {
		return LevelReport{}, err
	}
	return m.Report(), nil
}

// NormalizeLoudness applies one deterministic gain so the measured
// integrated loudness reaches targetLUFS, while guaranteeing the sample
// peak does not exceed peakCeilingDBFS. Digital silence is left unchanged
// and must not produce NaN/Inf.
func NormalizeLoudness(b *Buffer, targetLUFS, peakCeilingDBFS float64) (LevelReport, error) {
	before, err := Measure(b)
	if err != nil {
		return LevelReport{}, err
	}
	if before.Silent {
		return before, nil
	}
	gain := DBToLinear(targetLUFS - before.IntegratedLUFS)
	ceiling := DBToLinear(peakCeilingDBFS)
	if before.PeakLinear*gain > ceiling {
		gain = ceiling / before.PeakLinear
	}
	if math.IsNaN(gain) || math.IsInf(gain, 0) {
		return LevelReport{}, mederr.New(mederr.CatAudio, b.Layout.Name, -1, -1, false,
			"invalid normalization gain")
	}
	ApplyGain(b, gain)
	after, err := Measure(b)
	return after, err
}

// ApplyGain multiplies every sample by gain.
func ApplyGain(b *Buffer, gain float64) {
	if b == nil {
		return
	}
	for c := range b.Planar {
		for i := range b.Planar[c] {
			v := b.Planar[c][i] * gain
			if math.IsNaN(v) || math.IsInf(v, 0) {
				v = 0
			}
			b.Planar[c][i] = v
		}
	}
}

// DBToLinear converts a dB value to linear amplitude.
func DBToLinear(db float64) float64 { return math.Pow(10, db/20) }

// LinearToDB converts linear amplitude to dB with a finite floor.
func LinearToDB(v float64) float64 {
	if v <= 0 {
		return SilenceFloorDB
	}
	db := 20 * math.Log10(v)
	if db < SilenceFloorDB {
		return SilenceFloorDB
	}
	return db
}

func clampFinite(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < SilenceFloorDB {
		return SilenceFloorDB
	}
	return v
}

// biquad is a small transposed-direct-form-I filter section.
type biquad struct {
	b0, b1, b2 float64
	a1, a2     float64
	z1, z2     float64
}

func (q *biquad) process(x float64) float64 {
	y := q.b0*x + q.z1
	q.z1 = q.b1*x - q.a1*y + q.z2
	q.z2 = q.b2*x - q.a2*y
	return y
}

func newHighPass(freq, q float64, rate int) biquad {
	w0 := 2 * math.Pi * freq / float64(rate)
	cw, sw := math.Cos(w0), math.Sin(w0)
	alpha := sw / (2 * q)
	norm := 1 + alpha
	return biquad{
		b0: (1 + cw) / 2 / norm,
		b1: -(1 + cw) / norm,
		b2: (1 + cw) / 2 / norm,
		a1: -2 * cw / norm,
		a2: (1 - alpha) / norm,
	}
}

func newHighShelf(freq, q, gainDB float64, rate int) biquad {
	w0 := 2 * math.Pi * freq / float64(rate)
	cw, sw := math.Cos(w0), math.Sin(w0)
	a := math.Pow(10, gainDB/40)
	alpha := sw / 2 * math.Sqrt((a+1/a)*(1/q-1)+2)
	root2 := 2 * math.Sqrt(a) * alpha
	norm := (a + 1) + (a-1)*cw + root2
	return biquad{
		b0: a * ((a + 1) + -(a-1)*cw + root2) / norm,
		b1: 2 * a * ((a - 1) - (a+1)*cw) / norm,
		b2: a * ((a + 1) - (a-1)*cw - root2) / norm,
		a1: -2 * ((a - 1) + (a+1)*cw) / norm,
		a2: ((a + 1) + (a-1)*cw - root2) / norm,
	}
}
