// Package fixture builds in-memory WAV files for tests, demos and the synth
// command. Every byte is generated here; no external audio assets are used.
package fixture

import (
	"encoding/binary"
	"math"
	"math/rand"
)

// Encoding selects how samples are stored in the generated file.
type Encoding int

const (
	PCM16 Encoding = iota
	PCM24
	PCM32
	Float32
)

// String reports a human-readable encoding name.
func (e Encoding) String() string {
	switch e {
	case PCM16:
		return "pcm16"
	case PCM24:
		return "pcm24"
	case PCM32:
		return "pcm32"
	case Float32:
		return "float32"
	}
	return "unknown"
}

// ParseEncoding resolves an encoding name.
func ParseEncoding(s string) (Encoding, error) {
	switch s {
	case "pcm16", "16":
		return PCM16, nil
	case "pcm24", "24":
		return PCM24, nil
	case "pcm32", "32":
		return PCM32, nil
	case "float32", "f32", "float":
		return Float32, nil
	}
	return PCM16, ErrUnknownEncoding(s)
}

// ErrUnknownEncoding reports an unrecognized sample encoding name.
type ErrUnknownEncoding string

func (e ErrUnknownEncoding) Error() string { return "fixture: unknown encoding " + string(e) }

// Options controls the generated signal.
type Options struct {
	SampleRate int
	Channels   int
	Seconds    float64
	Encoding   Encoding
	// ToneDBFS is the sine amplitude in dBFS. A zero tone produces silence.
	ToneDBFS float64
	// ToneHz is the sine frequency. Defaults to 997 Hz, the BS.1770
	// calibration frequency.
	ToneHz float64
	// ToneChannels lists the channels that carry the tone; nil means all of
	// them. Channels not listed are silent.
	ToneChannels []int
	// Phase advances the sine start; useful for testing true-peak alignment.
	Phase float64
	// Extensible writes WAVE_FORMAT_EXTENSIBLE headers with a channel mask.
	Extensible bool
	// DataFirst writes the data chunk before the fmt chunk. The reader
	// accepts it, which is exactly why it is worth testing.
	DataFirst bool
	// OddJunk inserts an unknown chunk with an odd length (plus pad byte)
	// before fmt, exercising pad handling.
	OddJunk bool
}

// DefaultOptions returns the settings used by the synth command when the
// user does not override them.
func DefaultOptions() Options {
	return Options{
		SampleRate: 48000,
		Channels:   1,
		Seconds:    12,
		Encoding:   PCM24,
		ToneDBFS:   -23,
		ToneHz:     997,
	}
}

// Signal returns the interleaved float64 samples described by opts.
func Signal(opts Options) []float64 {
	if opts.SampleRate <= 0 || opts.Channels <= 0 || opts.Seconds <= 0 {
		return nil
	}
	frames := int(math.Round(opts.Seconds * float64(opts.SampleRate)))
	out := make([]float64, frames*opts.Channels)
	if opts.ToneHz <= 0 {
		opts.ToneHz = 997
	}
	amp := math.Pow(10, opts.ToneDBFS/20)
	if opts.ToneDBFS <= -999 {
		amp = 0
	}
	carry := make([]bool, opts.Channels)
	if opts.ToneChannels == nil {
		for i := range carry {
			carry[i] = true
		}
	} else {
		for _, c := range opts.ToneChannels {
			if c >= 0 && c < len(carry) {
				carry[c] = true
			}
		}
	}
	for n := 0; n < frames; n++ {
		v := amp * math.Sin(2*math.Pi*opts.ToneHz*float64(n)/float64(opts.SampleRate)+opts.Phase)
		for c := 0; c < opts.Channels; c++ {
			if carry[c] {
				out[n*opts.Channels+c] = v
			}
		}
	}
	return out
}

// Encode renders interleaved samples as a complete WAV file.
func Encode(opts Options, samples []float64) []byte {
	ch := opts.Channels
	if ch <= 0 {
		ch = 1
	}
	bits := 24
	isFloat := false
	switch opts.Encoding {
	case PCM16:
		bits = 16
	case PCM24:
		bits = 24
	case PCM32:
		bits = 32
	case Float32:
		bits = 32
		isFloat = true
	}
	blockAlign := ch * bits / 8
	dataSize := len(samples) * bits / 8

	var fmtChunk []byte
	if opts.Extensible {
		fmtChunk = make([]byte, 40)
		binary.LittleEndian.PutUint16(fmtChunk[0:], 0xFFFE)
		binary.LittleEndian.PutUint16(fmtChunk[2:], uint16(ch))
		binary.LittleEndian.PutUint32(fmtChunk[4:], uint32(opts.SampleRate))
		binary.LittleEndian.PutUint32(fmtChunk[8:], uint32(opts.SampleRate*blockAlign))
		binary.LittleEndian.PutUint16(fmtChunk[12:], uint16(blockAlign))
		binary.LittleEndian.PutUint16(fmtChunk[14:], uint16(bits))
		binary.LittleEndian.PutUint16(fmtChunk[16:], 22)
		binary.LittleEndian.PutUint16(fmtChunk[18:], uint16(bits))
		mask := channelMask(ch)
		binary.LittleEndian.PutUint32(fmtChunk[20:], mask)
		if isFloat {
			binary.LittleEndian.PutUint32(fmtChunk[24:], 3)
		} else {
			binary.LittleEndian.PutUint32(fmtChunk[24:], 1)
		}
		copy(fmtChunk[28:], []byte{0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71})
	} else {
		fmtChunk = make([]byte, 16)
		tag := uint16(1)
		if isFloat {
			tag = 3
		}
		binary.LittleEndian.PutUint16(fmtChunk[0:], tag)
		binary.LittleEndian.PutUint16(fmtChunk[2:], uint16(ch))
		binary.LittleEndian.PutUint32(fmtChunk[4:], uint32(opts.SampleRate))
		binary.LittleEndian.PutUint32(fmtChunk[8:], uint32(opts.SampleRate*blockAlign))
		binary.LittleEndian.PutUint16(fmtChunk[12:], uint16(blockAlign))
		binary.LittleEndian.PutUint16(fmtChunk[14:], uint16(bits))
	}

	data := make([]byte, 0, dataSize)
	for _, s := range samples {
		data = appendSample(data, s, opts.Encoding)
	}

	var body []byte
	appendChunk := func(id string, payload []byte) {
		body = append(body, id...)
		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], uint32(len(payload)))
		body = append(body, sz[:]...)
		body = append(body, payload...)
		if len(payload)%2 == 1 {
			body = append(body, 0)
		}
	}
	appendUnknown := func() {
		// The payload is deliberately odd-length (23 bytes) so the pad byte
		// is actually exercised. Emitting a pad after an even payload would
		// shift every later chunk and corrupt the file.
		payload := []byte("odd-payload-for-loudcheck")
		body = append(body, "JUNK"...)
		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], uint32(len(payload)))
		body = append(body, sz[:]...)
		body = append(body, payload...)
		if len(payload)%2 == 1 {
			body = append(body, 0)
		}
	}
	if opts.OddJunk {
		appendUnknown()
	}
	if opts.DataFirst {
		appendChunk("data", data)
		appendChunk("fmt ", fmtChunk)
	} else {
		appendChunk("fmt ", fmtChunk)
		appendChunk("data", data)
	}

	out := make([]byte, 0, 12+len(body))
	out = append(out, "RIFF"...)
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(4+len(body)))
	out = append(out, sz[:]...)
	out = append(out, "WAVE"...)
	out = append(out, body...)
	return out
}

func appendSample(dst []byte, s float64, enc Encoding) []byte {
	switch enc {
	case Float32:
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(float32(s)))
		return append(dst, b[:]...)
	case PCM16:
		v := int32(math.Round(clamp(s) * 32767))
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(int16(v)))
		return append(dst, b[:]...)
	case PCM32:
		v := int64(math.Round(clamp(s) * 2147483647))
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(int32(v)))
		return append(dst, b[:]...)
	default: // PCM24
		v := int32(math.Round(clamp(s) * 8388607))
		return append(dst, byte(v), byte(v>>8), byte(v>>16))
	}
}

func clamp(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

func channelMask(ch int) uint32 {
	switch ch {
	case 1:
		return 0x4 // FC
	case 2:
		return 0x3 // FL FR
	case 6:
		return 0x3F // FL FR FC LFE BL BR
	case 8:
		return 0x63F // FL FR FC LFE BL BR SL SR
	}
	return 0
}

// Noise returns a deterministic pink-ish noise signal for tests that need a
// non-tonal stimulus. The same seed always yields the same samples.
func Noise(opts Options, seed int64, dbfs float64) []float64 {
	frames := int(math.Round(opts.Seconds * float64(opts.SampleRate)))
	out := make([]float64, frames*opts.Channels)
	rng := rand.New(rand.NewSource(seed))
	amp := math.Pow(10, dbfs/20)
	for i := range out {
		out[i] = (rng.Float64()*2 - 1) * amp
	}
	return out
}
