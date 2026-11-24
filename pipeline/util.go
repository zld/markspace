package pipeline

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
)

// bytesReader wraps the fixture buffer without keeping a second copy.
func bytesReader(fx *Fixtures) *bytes.Buffer { return &fx.TS }

// synthMono builds a deterministic mono sine block in [-amp, amp].
func synthMono(n int, amp float32) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = amp * float32(math.Sin(2*math.Pi*440*float64(i)/48000))
	}
	return out
}

// hashBytes produces a stable 16-char fingerprint of any structured value.
func hashBytes(v any) string {
	h := fnv.New128a()
	switch x := v.(type) {
	case []float32:
		var buf [4]byte
		for _, f := range x {
			binary.BigEndian.PutUint32(buf[:], math.Float32bits(f))
			h.Write(buf[:])
		}
	case []byte:
		h.Write(x)
	default:
		fmt.Fprintf(h, "%v", x)
	}
	return bytesToHex(h.Sum(nil))[:16]
}

func bytesToHex(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexd[c>>4]
		out[i*2+1] = hexd[c&0x0F]
	}
	return string(out)
}
