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

// readFloatBlocks streams the spool back in blocks of whole frames.
func readFloatBlocks(r io.Reader, channels int, fn func([]float64) error) error {
	const framesPerBlock = 4096
	bytesPerBlock := framesPerBlock * 8
	buf := make([]byte, bytesPerBlock)
	var carry []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			carry = append(carry, buf[:n]...)
			whole := len(carry) / 8
			if whole > 0 {
				samples := make([]float64, whole)
				for i := 0; i < whole; i++ {
					samples[i] = math.Float64frombits(binary.LittleEndian.Uint64(carry[i*8:]))
				}
				block := make([]float64, len(samples))
				copy(block, samples)
				if err := fn(block); err != nil {
					return err
				}
				carry = append(carry[:0], carry[whole*8:]...)
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
		return fmt.Errorf("pipeline: %d trailing spool bytes are not a whole sample", len(carry))
	}
	return nil
}
