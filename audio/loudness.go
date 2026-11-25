package audio

import (
	"encoding/binary"
	"math"

	"github.com/zld/media-regress/internal/error"
)

// Conversion is a streaming conversion of one track. Blocks must be fed in
// order; the converter keeps only the resampler phase and the cumulative
// measured energy, never the audio itself.
type Conversion struct {
	conv      *Converter
	resample  *resampler
	nextBlock int
	// accumulated energy for loudness measurement, in LUFS terms
	sumSquares float64
	framesSeen int64
	total      int64
	truncated  bool
}

// Start begins a track conversion.
func (c *Converter) Start(sourceFrames int64) *Conversion {
	return &Conversion{
		conv:     c,
		resample: newResampler(c.SourceRate, c.TargetRate),
		total:    sourceFrames,
	}
}

// ConvertBlock converts one source block. srcStart is the block's first source
// frame. A block that ends mid-frame is reported as truncated and its trailing
// bytes are dropped, never carried into the next block.
func (cv *Conversion) ConvertBlock(block []byte, srcStart int64, index int, inputOffset int64) (Block, error) {
	if index != cv.nextBlock {
		return Block{}, merr.New(merr.StageAudio, merr.KindInconsistent, 0, inputOffset,
			"audio block %d arrived out of order (next %d)", index+1, cv.nextBlock+1)
	}
	cv.nextBlock++
	src, trailing, err := cv.conv.sourceFrames(block, index, inputOffset)
	if err != nil {
		return Block{}, err
	}
	nFrames := int64(0)
	if len(src) > 0 {
		nFrames = int64(len(src[0]))
	}
	positions := cv.resample.blockPositions(srcStart, nFrames)
	out := Block{
		Index:        index,
		Start:        positionsOut(positions),
		SourceFrames: nFrames,
		Frames:       len(positions),
		Truncated:    trailing != 0,
	}
	if trailing != 0 {
		cv.truncated = true
	}
	out.PCM = make([]byte, 0, out.Frames*len(cv.conv.TargetLayout.Channels)*2)
	for _, p := range positions {
		mixed := cv.conv.mixFrame(src, int(p.In-srcStart))
		for _, v := range mixed {
			g := cv.conv.gainSample(v)
			var b [2]byte
			binary.LittleEndian.PutUint16(b[:], uint16(g))
			out.PCM = append(out.PCM, b[:]...)
			f := float64(g) / 32768.0
			cv.sumSquares += f * f
		}
		cv.framesSeen++
	}
	return out, nil
}

func positionsOut(p []struct{ Out, In int64 }) int64 {
	if len(p) == 0 {
		return 0
	}
	return p[0].Out
}

// Truncated reports whether any block ended mid-frame.
func (cv *Conversion) Truncated() bool { return cv.truncated }

// InputLoudness returns the integrated loudness of the converted output so far
// in LUFS (ITU-R BS.1770 style with a -0.691 offset for a single channel).
func (cv *Conversion) InputLoudness() float64 {
	if cv.sumSquares <= 0 || cv.framesSeen == 0 {
		return -70
	}
	channels := int64(len(cv.conv.TargetLayout.Channels))
	mean := cv.sumSquares / float64(cv.framesSeen*channels)
	if mean <= 0 {
		return -70
	}
	lufs := -0.691 + 10*math.Log10(mean)
	if lufs < -70 {
		return -70
	}
	return lufs
}

// ApplyGain sets the gain needed to reach the configured loudness target given
// the measured input loudness. A zero target disables normalisation.
func (c *Converter) ApplyGain(measuredLUFS float64) error {
	if c.TargetLUFS == 0 {
		c.GainDB = 0
		return nil
	}
	if measuredLUFS <= -70 {
		// A silent track cannot be normalised; reject rather than amplify noise.
		return merr.New(merr.StageAudio, merr.KindInconsistent, 0, 0,
			"cannot normalise silent input to %.2f LUFS", c.TargetLUFS)
	}
	gain := c.TargetLUFS - measuredLUFS
	if math.Abs(gain) > 60 {
		return merr.New(merr.StageAudio, merr.KindLimit, 0, 0,
			"required loudness gain %.2f dB exceeds limit", gain)
	}
	c.GainDB = gain
	return nil
}
