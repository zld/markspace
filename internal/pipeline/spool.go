package pipeline

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// writeFloatBlock appends float64 samples to an open file as little-endian
// float64 words.  The spool format is internal only and never shipped.
func writeFloatBlock(w io.Writer, samples []float64) error {
	buf := make([]byte, 8*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint64(buf[i*8:], math.Float64bits(v))
	}
	_, err := w.Write(buf)
	return err
}

// readFloatBlocks streams the spool back in blocks of whole frames so every
// callback receives a length divisible by channels.  Leftover samples that do
// not complete a frame are held across reads and reported if they remain at
// end of stream.
func readFloatBlocks(r io.Reader, channels int, fn func([]float64) error) error {
	if channels <= 0 {
		return fmt.Errorf("pipeline: invalid channel count %d", channels)
	}
	const framesPerBlock = 4096
	bytesPerBlock := framesPerBlock * 8
	buf := make([]byte, bytesPerBlock)
	var carry []byte
	flush := func() error {
		frames := len(carry) / (8 * channels)
		if frames == 0 {
			return nil
		}
		n := frames * channels // samples
		samples := make([]float64, n)
		for i := 0; i < n; i++ {
			samples[i] = math.Float64frombits(binary.LittleEndian.Uint64(carry[i*8:]))
		}
		if err := fn(samples); err != nil {
			return err
		}
		carry = append(carry[:0], carry[n*8:]...)
		return nil
	}
	for {
		got, err := r.Read(buf)
		if got > 0 {
			carry = append(carry, buf[:got]...)
			if err := flush(); err != nil {
				return err
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if len(carry) != 0 {
		return fmt.Errorf("pipeline: %d trailing spool bytes do not form a whole frame", len(carry))
	}
	return nil
}
