package audio

import (
	"encoding/binary"
	"math"

	"github.com/zld/media-regress/internal/error"
)

// PCM decoding is strict: a block whose byte length is not an exact multiple
// of the source frame size is not padded or guessed at. The converter reports
// how many trailing bytes were dropped so the caller can surface truncation.

// sourceFrames decodes interleaved s16le into per-channel source frames.
// It returns the frames and the number of trailing bytes that did not form a
// complete frame.
func (c *Converter) sourceFrames(block []byte, index int, offset int64) ([][]int16, int, error) {
	frameBytes := len(c.SourceLayout.Channels) * 2
	if frameBytes == 0 {
		return nil, 0, merr.New(merr.StageAudio, merr.KindInconsistent, 0, offset, "source layout has no channels")
	}
	usable := len(block) - len(block)%frameBytes
	trailing := len(block) - usable
	frames := usable / frameBytes
	_ = index
	chans := make([][]int16, len(c.SourceLayout.Channels))
	for ch := range chans {
		chans[ch] = make([]int16, frames)
	}
	for f := 0; f < frames; f++ {
		for ch := range chans {
			pos := f*frameBytes + ch*2
			chans[ch][f] = int16(binary.LittleEndian.Uint16(block[pos : pos+2]))
		}
	}
	return chans, trailing, nil
}

// mixFrame produces one output frame from the source channels at time f.
func (c *Converter) mixFrame(src [][]int16, f int) []int16 {
	dst := make([]int16, len(c.remap))
	for i, r := range c.remap {
		if len(r.Sources) == 0 {
			dst[i] = 0
			continue
		}
		var sum int64
		for _, s := range r.Sources {
			sum += int64(src[s][f])
		}
		dst[i] = clamp16(float64(sum) / float64(len(r.Sources)))
	}
	return dst
}

// resamplePositions maps output frame indices to exact source positions for a
// block starting at source frame srcStart and covering srcFrames frames.
// Positions are expressed in units of 1/outLen of a source frame to stay in
// integer arithmetic: out frames are emitted while the mapped source position
// is inside [srcStart, srcStart+srcFrames).
type resampler struct {
	inRate, outRate int64
	// phase is the fractional source position carried between blocks in units
	// of outRate: sourcePos = inFrame + phase/outRate.
	phase int64
}

// reset sets up a resampler for a full track.
func newResampler(inRate, outRate uint32) *resampler {
	return &resampler{inRate: int64(inRate), outRate: int64(outRate)}
}

// blockPositions returns, for a source block [srcStart, srcStart+n), the output
// frames whose source position falls in the block, plus the source position of
// each. All arithmetic is integer so results are reproducible.
func (r *resampler) blockPositions(srcStart int64, n int64) []struct{ Out, In int64 } {
	var out []struct{ Out, In int64 }
	// Global output frame k maps to source position k*inRate/outRate. We keep
	// an explicit global counter so blocks tile without gaps or overlaps.
	k := r.firstOut(srcStart)
	for {
		srcPos := k * r.inRate / r.outRate
		if srcPos >= srcStart+n {
			break
		}
		if srcPos >= srcStart {
			out = append(out, struct{ Out, In int64 }{Out: k, In: srcPos})
		}
		k++
	}
	return out
}

// firstOut returns the first output frame whose source position is >= srcStart.
func (r *resampler) firstOut(srcStart int64) int64 {
	// Smallest k with k*inRate >= srcStart*outRate.
	num := srcStart * r.outRate
	return (num + r.inRate - 1) / r.inRate
}

// totalOut returns the number of output frames covering n source frames.
func totalOut(n int64, inRate, outRate uint32) int64 {
	return n * int64(outRate) / int64(inRate)
}

// clamp16 rounds half away from zero and saturates to the s16 range.
func clamp16(v float64) int16 {
	if v >= math.MaxInt16 {
		return math.MaxInt16
	}
	if v <= math.MinInt16 {
		return math.MinInt16
	}
	if v >= 0 {
		return int16(math.Floor(v + 0.5))
	}
	return int16(math.Ceil(v - 0.5))
}

// gainSample applies the converter gain.
func (c *Converter) gainSample(v int16) int16 {
	if c.GainDB == 0 {
		return v
	}
	return clamp16(float64(v) * math.Pow(10, c.GainDB/20))
}
