package ebu

import (
	"errors"
	"math"
)

const (
	// loudnessOffset is the BS.1770 constant term.
	loudnessOffset = -0.691
	// absoluteGateLUFS is the absolute gate applied before any relative gating.
	absoluteGateLUFS = -70.0
	// relativeGateLU is subtracted from the loudness of the absolute-gated
	// blocks to form the relative gate.
	relativeGateLU = 10.0
	// ShortBlockSeconds is the short-term measurement window.
	ShortBlockSeconds = 0.4
	// ShortHopSeconds is the spacing between short-term windows.
	ShortHopSeconds = 0.1
	// IntegratedBlockSeconds is the integration window.
	IntegratedBlockSeconds = 3.0
)

// block is one measurement window: the sum of weighted mean-square values and
// the number of frames it covers. Keeping the count lets a trailing partial
// window be weighted by its real duration instead of being padded with zeros.
type block struct {
	sum   float64
	count int64
}

func (b block) energy() float64 {
	if b.count == 0 {
		return 0
	}
	return b.sum / float64(b.count)
}

func energyToLUFS(e float64) float64 {
	if e <= 0 {
		return math.Inf(-1)
	}
	return loudnessOffset + 10*math.Log10(e)
}

// Meter computes short-term and integrated loudness for one file. It is not
// safe for concurrent use. Filter state is continuous for the lifetime of the
// meter; callers must not rebuild it between chunks.
type Meter struct {
	sampleRate int
	layout     Layout
	chains     []*Chain // nil entries are unweighted channels (LFE)
	weights    []float64

	segLen   int64 // 100 ms in frames
	segSum   float64
	segCount int64
	// ring holds the last windowSegments segments; their sum is one 400 ms
	// short-term window.
	ring        []block
	ringPos     int
	ringFilled  int
	shortBlocks []block

	integratedSamples int64
	intSum            float64
	intCount          int64
	intBlocks         []block

	totalSum   float64
	totalCount int64
}

// NewMeter validates the sample rate against the K-weighting table and builds
// one independent filter chain per weighted channel.
func NewMeter(sampleRate int, layout Layout) (*Meter, error) {
	kw, err := KWeightingFor(sampleRate)
	if err != nil {
		return nil, err
	}
	if layout.Len() == 0 {
		return nil, errors.New("empty channel layout")
	}
	windowSegments := int(math.Round(ShortBlockSeconds / ShortHopSeconds))
	if windowSegments < 1 {
		return nil, errors.New("short-term window is shorter than its hop")
	}
	m := &Meter{
		sampleRate:        sampleRate,
		layout:            layout,
		chains:            make([]*Chain, layout.Len()),
		weights:           make([]float64, layout.Len()),
		segLen:            int64(math.Round(float64(sampleRate) * ShortHopSeconds)),
		ring:              make([]block, windowSegments),
		integratedSamples: int64(math.Round(float64(sampleRate) * IntegratedBlockSeconds)),
	}
	if m.segLen < 1 {
		return nil, errors.New("sample rate too low for 100 ms blocks")
	}
	for i := 0; i < layout.Len(); i++ {
		m.weights[i] = layout.Weight(i)
		if m.weights[i] > 0 {
			m.chains[i] = NewChain(kw)
		}
	}
	return m, nil
}

// ProcessInterleaved filters and accumulates interleaved PCM frames. The
// caller may pass frames in arbitrary chunk sizes; state is preserved.
func (m *Meter) ProcessInterleaved(frames []float64) error {
	ch := m.layout.Len()
	if len(frames)%ch != 0 {
		return errors.New("pcm buffer is not a whole number of frames")
	}
	for i := 0; i < len(frames); i += ch {
		var energy float64
		for c := 0; c < ch; c++ {
			x := frames[i+c]
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return errors.New("non-finite sample in decoded pcm")
			}
			chain := m.chains[c]
			if chain == nil {
				continue
			}
			y := chain.Process(x)
			energy += m.weights[c] * y * y
		}
		if math.IsInf(energy, 1) {
			return errors.New("sample energy overflow")
		}
		m.accumulate(energy)
	}
	return nil
}

func (m *Meter) accumulate(energy float64) {
	m.totalSum += energy
	m.totalCount++
	m.segSum += energy
	m.segCount++
	m.intSum += energy
	m.intCount++

	if m.segCount == m.segLen {
		m.closeSegment()
	}
	if m.intCount >= m.integratedSamples {
		m.closeIntegratedBlock()
	}
}

func (m *Meter) closeSegment() {
	m.ring[m.ringPos] = block{sum: m.segSum, count: m.segCount}
	m.ringPos = (m.ringPos + 1) % len(m.ring)
	if m.ringFilled < len(m.ring) {
		m.ringFilled++
	}
	if m.ringFilled == len(m.ring) {
		var sum float64
		var count int64
		for _, b := range m.ring {
			sum += b.sum
			count += b.count
		}
		m.shortBlocks = append(m.shortBlocks, block{sum: sum, count: count})
	}
	m.segSum = 0
	m.segCount = 0
}

func (m *Meter) closeIntegratedBlock() {
	if m.intCount == 0 {
		return
	}
	m.intBlocks = append(m.intBlocks, block{sum: m.intSum, count: m.intCount})
	m.intSum = 0
	m.intCount = 0
}

// Result is the outcome of a loudness measurement.
type Result struct {
	IntegratedLUFS   float64
	MaxShortTermLUFS float64
	Measured         bool
	TotalFrames      int64
	IntegratedBlocks int
	ShortBlocks      int
	PartialShort     bool // the file was shorter than one 400 ms window
	PartialIntegrate bool // the trailing integrated window was partial
}

// Finish closes the trailing windows and applies the absolute and relative
// gates. It never returns NaN: unmeasurable material yields -Inf and
// Measured == false.
func (m *Meter) Finish() Result {
	res := Result{
		IntegratedLUFS:   math.Inf(-1),
		MaxShortTermLUFS: math.Inf(-1),
		TotalFrames:      m.totalCount,
	}

	// Short-term: use completed 400 ms windows. For material shorter than one
	// window, measure the real samples over their actual duration rather than
	// fabricating a window with silence.
	if len(m.shortBlocks) == 0 && m.totalCount > 0 {
		m.shortBlocks = append(m.shortBlocks, block{sum: m.totalSum, count: m.totalCount})
		res.PartialShort = true
	}
	for _, b := range m.shortBlocks {
		l := energyToLUFS(b.energy())
		if l > absoluteGateLUFS && l > res.MaxShortTermLUFS {
			res.MaxShortTermLUFS = l
		}
	}
	res.ShortBlocks = len(m.shortBlocks)

	blocks := m.intBlocks
	if m.intCount > 0 {
		blocks = append(blocks, block{sum: m.intSum, count: m.intCount})
		res.PartialIntegrate = true
	}
	res.IntegratedBlocks = len(blocks)

	// Absolute gate first.
	abs := make([]block, 0, len(blocks))
	for _, b := range blocks {
		if energyToLUFS(b.energy()) > absoluteGateLUFS {
			abs = append(abs, b)
		}
	}
	if len(abs) == 0 {
		return res
	}

	// Relative gate derived from the absolute-gated set.
	relGate := energyToLUFS(weightedEnergy(abs)) - relativeGateLU
	final := make([]block, 0, len(abs))
	for _, b := range abs {
		if energyToLUFS(b.energy()) > relGate {
			final = append(final, b)
		}
	}
	if len(final) == 0 {
		// Cannot happen for a non-empty abs set, but never report NaN.
		return res
	}
	res.IntegratedLUFS = energyToLUFS(weightedEnergy(final))
	res.Measured = true
	return res
}

func weightedEnergy(blocks []block) float64 {
	var sum float64
	var count int64
	for _, b := range blocks {
		sum += b.sum
		count += b.count
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}
