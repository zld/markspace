package measure

import (
	"fmt"
	"math"

	"loudcheck/internal/filter"
)

// Gating constants (BS.1770 two-stage gating).
const (
	// AbsoluteGateLUFS is the absolute gating threshold.
	AbsoluteGateLUFS = -70.0
	// RelativeGateLU is the relative gate offset below the ungated mean.
	RelativeGateLU = -10.0
	// loudnessOffset is the BS.1770 scale constant.
	loudnessOffset = 0.691
	// windowSegs is the 400 ms block length in 100 ms segments.
	windowSegs = 4
)

// Loudness status values reported in LoudnessResult.Status.
const (
	StatusOK        = "ok"
	StatusEmpty     = "empty"
	StatusBelowGate = "below_gate"
)

// LoudnessResult summarizes a gated loudness measurement. Nil pointers
// mean "no valid measurement" and serialize as JSON null.
type LoudnessResult struct {
	Integrated   *float64 // LUFS after absolute+relative gating
	ShortTermMax *float64 // max 400 ms block loudness (LUFS)
	BlocksTotal  int      // 400 ms blocks emitted (100 ms hop)
	BlocksAbs    int      // blocks above the absolute gate
	BlocksRel    int      // blocks above absolute and relative gates
	Status       string
}

// LoudnessMeter streams K-weighted audio into 400 ms gating blocks with
// a 100 ms hop. Memory use is bounded: O(channels) state plus one
// float64 per 100 ms of program.
type LoudnessMeter struct {
	weights  []float64
	filters  []*filter.KWeight
	segLen   int // frames per 100 ms segment
	segAcc   []float64
	segPos   int
	ring     [][]float64 // per channel, last windowSegs segment sums
	ringPos  int
	segments int64
	frames   int64
	blocks   []float64 // weighted mean square per block
	stMax    float64
	hasST    bool
	finished bool
}

// NewLoudnessMeter builds a meter for the given sample rate and weights.
func NewLoudnessMeter(sampleRate int, weights []float64) (*LoudnessMeter, error) {
	segLen := sampleRate / 10
	if segLen < 1 {
		return nil, fmt.Errorf("sample rate %d Hz too low for 100 ms segments", sampleRate)
	}
	filters := make([]*filter.KWeight, len(weights))
	for i := range filters {
		k, err := filter.NewKWeight(sampleRate)
		if err != nil {
			return nil, err
		}
		filters[i] = k
	}
	ring := make([][]float64, len(weights))
	for i := range ring {
		ring[i] = make([]float64, windowSegs)
	}
	return &LoudnessMeter{
		weights: weights, filters: filters, segLen: segLen,
		segAcc: make([]float64, len(weights)), ring: ring,
	}, nil
}

// AddBlock feeds one deinterleaved block (samples[channel][frame]).
func (m *LoudnessMeter) AddBlock(samples [][]float64) {
	if len(samples) == 0 {
		return
	}
	for i := 0; i < len(samples[0]); i++ {
		for c := range samples {
			y := m.filters[c].Process(samples[c][i])
			m.segAcc[c] += y * y
		}
		m.segPos++
		m.frames++
		if m.segPos == m.segLen {
			m.finishSegment()
		}
	}
}

func (m *LoudnessMeter) finishSegment() {
	for c := range m.ring {
		m.ring[c][m.ringPos] = m.segAcc[c]
		m.segAcc[c] = 0
	}
	m.ringPos = (m.ringPos + 1) % windowSegs
	m.segments++
	m.segPos = 0
	if m.segments >= windowSegs {
		var sum float64
		for c := range m.ring {
			var s float64
			for _, v := range m.ring[c] {
				s += v
			}
			sum += m.weights[c] * s
		}
		m.emit(sum, windowSegs*m.segLen)
	}
}

func (m *LoudnessMeter) emit(weightedSquares float64, frames int) {
	e := weightedSquares / float64(frames)
	m.blocks = append(m.blocks, e)
	if e > 0 {
		if l := 10*math.Log10(e) - loudnessOffset; !m.hasST || l > m.stMax {
			m.stMax, m.hasST = l, true
		}
	}
}

// Finish flushes a partial window for material shorter than 400 ms.
func (m *LoudnessMeter) Finish() {
	if m.finished {
		return
	}
	m.finished = true
	if m.frames == 0 || m.segments >= windowSegs {
		return
	}
	var sum float64
	for c := range m.ring {
		var s float64
		for _, v := range m.ring[c] {
			s += v
		}
		sum += m.weights[c] * (s + m.segAcc[c])
	}
	m.emit(sum, int(m.frames))
}

// Result applies the two-stage gate and returns the final measurement.
func (m *LoudnessMeter) Result() LoudnessResult {
	m.Finish()
	res := LoudnessResult{BlocksTotal: len(m.blocks), Status: StatusOK}
	if m.frames == 0 {
		res.Status = StatusEmpty
		return res
	}
	if m.hasST {
		v := m.stMax
		res.ShortTermMax = &v
	}
	absE := math.Pow(10, (AbsoluteGateLUFS+loudnessOffset)/10)
	var sum1 float64
	for _, e := range m.blocks {
		if e > absE {
			sum1 += e
			res.BlocksAbs++
		}
	}
	if res.BlocksAbs == 0 {
		res.Status = StatusBelowGate
		return res
	}
	relE := sum1 / float64(res.BlocksAbs) * math.Pow(10, RelativeGateLU/10)
	threshold := math.Max(absE, relE)
	var sum2 float64
	for _, e := range m.blocks {
		if e > threshold {
			sum2 += e
			res.BlocksRel++
		}
	}
	v := 10*math.Log10(sum2/float64(res.BlocksRel)) - loudnessOffset
	res.Integrated = &v
	return res
}
