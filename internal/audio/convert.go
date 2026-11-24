package audio

import "math"

// BlockSize is the conversion window in source frames. It divides every rate
// used in the generated fixtures so block boundaries stay sample-accurate.
const BlockSize = 480

// Resampler converts mono PCM from a source rate to a target rate in fixed
// blocks. A short tail that cannot fill one block is retained until the next
// call; Flush zero-pads the final partial block and flags the boundary so no
// caller ever reads beyond the real sample range.
type Resampler struct {
	srcRate int
	dstRate int
	carry   []int16
}

// NewResampler validates the rates and builds a mono resampler.
func NewResampler(srcRate, dstRate int) (*Resampler, error) {
	if srcRate <= 0 || dstRate <= 0 {
		return nil, ErrBadRate
	}
	return &Resampler{srcRate: srcRate, dstRate: dstRate}, nil
}

// OutFramesPerBlock is the exact number of target frames each full block yields.
func (r *Resampler) OutFramesPerBlock() int {
	return int(math.Round(float64(BlockSize) * float64(r.dstRate) / float64(r.srcRate)))
}

// ResampleBlock appends incoming mono frames, emits zero or more full blocks,
// and retains the short tail. Output frames are deterministic linear
// interpolation; integer and fractional ratios use the same code path.
func (r *Resampler) ResampleBlock(in []int16) []int16 {
	r.carry = append(r.carry, in...)
	var out []int16
	for len(r.carry) >= BlockSize {
		block := r.carry[:BlockSize]
		r.carry = r.carry[BlockSize:]
		out = append(out, r.convert(block)...)
	}
	return out
}

// convert resamples exactly one BlockSize-sized source block.
func (r *Resampler) convert(block []int16) []int16 {
	n := r.OutFramesPerBlock()
	out := make([]int16, n)
	if r.srcRate == r.dstRate {
		copy(out, block[:n])
		return out
	}
	for i := 0; i < n; i++ {
		// Position of output i in source coordinates.
		pos := float64(i) * float64(r.srcRate) / float64(r.dstRate)
		idx := int(math.Floor(pos))
		frac := pos - math.Floor(pos)
		if idx >= BlockSize-1 {
			out[i] = block[BlockSize-1]
			continue
		}
		v := float64(block[idx])*(1-frac) + float64(block[idx+1])*frac
		out[i] = clampInt16(v)
	}
	return out
}

// Flush emits the retained tail. The boolean result is true when the tail was
// shorter than a full conversion window, marking a partial block boundary.
func (r *Resampler) Flush() ([]int16, bool) {
	if len(r.carry) == 0 {
		return nil, false
	}
	tailLen := len(r.carry)
	partial := tailLen < BlockSize
	block := make([]int16, BlockSize)
	copy(block, r.carry) // zero-padded tail, never an out-of-range read
	full := r.convert(block)
	r.carry = r.carry[:0]
	if !partial {
		return full, false
	}
	// Keep only target frames covered by the real (non-padded) tail.
	real := int(math.Round(float64(tailLen) * float64(r.dstRate) / float64(r.srcRate)))
	if real < 1 {
		real = 1
	}
	if real > len(full) {
		real = len(full)
	}
	return full[:real], true
}

// Carried returns the number of source frames waiting for the next block.
func (r *Resampler) Carried() int { return len(r.carry) }

func clampInt16(v float64) int16 {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(math.Round(v))
}
