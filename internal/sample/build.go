package sample

import (
	"fmt"

	"archivecheck/internal/codec"
)

// Fixture cadence.
const (
	frameIntervalMs  = 50
	keyframeWeight   = 6
	interFrameWeight = 1
)

// prng is a small deterministic xorshift64* generator: generated media must be
// reproducible byte for byte across runs and architectures.
type prng struct{ state uint64 }

func newPRNG(seed int64) *prng {
	s := uint64(seed)
	if s == 0 {
		s = 0x9E3779B97F4A7C15
	}
	return &prng{state: s}
}

func (p *prng) next() uint64 {
	x := p.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	p.state = x
	return x * 0x2545F4914F6CDD1D
}

// nonZero returns a byte in [1,255]. Zero is never produced so payloads cannot
// contain accidental Annex B prefixes.
func (p *prng) nonZero() byte { return byte(p.next()>>33%255 + 1) }

func (p *prng) fill(n int64) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = p.nonZero()
	}
	return b
}

func (p *prng) fraction() float64 { return float64(p.next()>>11) / float64(1<<53) }

// built is the encoded result of one generated layer.
type built struct {
	stream  []byte
	offsets []int64 // every frame
	lengths []int64 // every frame
	times   []int64 // every frame
	keys    []bool  // every frame
}

type builder struct {
	codec codec.Codec
	rng   *prng
	total int64
}

func newBuilder(c codec.Codec, seed, total int64) *builder {
	return &builder{codec: c, rng: newPRNG(seed), total: total}
}

func lebLen(v int64) int64 {
	n := int64(1)
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

// unitOverhead is the header cost of one frame unit with the given payload.
func (b *builder) unitOverhead(payload int64, key bool) int64 {
	switch b.codec {
	case codec.H264:
		return 5 // 4-byte start code + header
	case codec.H265:
		return 6 // 4-byte start code + 2-byte header
	default:
		typ := uint8(codec.OBUTileGroup)
		if key {
			typ = codec.OBUFrame
		}
		_ = typ
		return 2 + lebLen(payload) // header byte + size field
	}
}

func (b *builder) frameUnit(key bool, payload int64) []byte {
	switch b.codec {
	case codec.H264:
		hdr := byte(0x41) // non-IDR slice
		if key {
			hdr = 0x65 // IDR slice
		}
		return append([]byte{0, 0, 0, 1, hdr}, b.rng.fill(payload)...)
	case codec.H265:
		typ := byte(1) // TRAIL_R
		if key {
			typ = 19 // IDR_W_RADL
		}
		return append([]byte{0, 0, 0, 1, typ << 1, 0x01}, b.rng.fill(payload)...)
	default:
		typ := uint8(codec.OBUTileGroup)
		if key {
			typ = codec.OBUFrame
		}
		return codec.EncodeOBU(typ, b.rng.fill(payload))
	}
}

// fillerUnit builds a non-keyframe unit of exactly n bytes, if possible.
func (b *builder) fillerUnit(n int64) ([]byte, bool) {
	switch b.codec {
	case codec.H264:
		if n < 5 {
			return nil, false
		}
		return append([]byte{0, 0, 0, 1, 0x0C}, b.rng.fill(n-5)...), true
	case codec.H265:
		if n < 6 {
			return nil, false
		}
		return append([]byte{0, 0, 0, 1, 0x4C, 0x01}, b.rng.fill(n-6)...), true
	default:
		for payload := int64(0); payload <= n; payload++ {
			if 1+lebLen(payload)+payload == n {
				return codec.EncodeOBU(codec.OBUPadding, b.rng.fill(payload)), true
			}
		}
		return nil, false
	}
}

// splitFiller builds a leading unit that ends two bytes before a chunk
// boundary so the following start code spans two chunks.
func (b *builder) splitFiller(chunkBytes int) []byte {
	if b.codec == codec.AV1 || chunkBytes < 12 {
		return nil
	}
	target := int64(chunkBytes - 2)
	u, ok := b.fillerUnit(target)
	if !ok {
		return nil
	}
	return u
}

// build lays out frames on a fixed cadence and returns the exact-size stream.
// Keyframe offsets are recorded before each unit; times are absolute
// milliseconds from the start.
func (b *builder) build(durationMs, kfIntervalMs int64, leading []byte) (built, error) {
	if durationMs%frameIntervalMs != 0 || kfIntervalMs%frameIntervalMs != 0 {
		return built{}, fmt.Errorf("sample: duration and keyframe interval must be multiples of %d ms", frameIntervalMs)
	}
	frames := int(durationMs / frameIntervalMs)
	if frames == 0 {
		return built{}, fmt.Errorf("sample: duration too short")
	}
	var out []byte
	out = append(out, leading...)
	if b.codec == codec.AV1 {
		out = append(out, codec.EncodeOBU(codec.OBUSequenceHeader, b.rng.fill(24))...)
	}

	// The trailing filler is large enough to absorb injected truncation
	// without touching real frames.
	tailReserve := int64(64)
	if b.codec == codec.AV1 {
		tailReserve = 1200
	}
	if b.total <= tailReserve+int64(len(out))+int64(frames)*8 {
		return built{}, fmt.Errorf("sample: target too small for %d frames", frames)
	}

	keyFlags := make([]bool, frames)
	weights := make([]int64, frames)
	var totalWeight int64
	for i := 0; i < frames; i++ {
		key := (int64(i)*frameIntervalMs)%kfIntervalMs == 0
		keyFlags[i] = key
		weights[i] = interFrameWeight
		if key {
			weights[i] = keyframeWeight
		}
		totalWeight += weights[i]
	}

	// Content budget: enough for frames plus the reserve, leaving the rest as
	// filler. The small deterministic margin keeps bitrate inside ±5%.
	content := b.total - tailReserve
	margin := int64(0)
	if content > 8192 {
		margin = int64(float64(content) * 0.01 * b.rng.fraction())
	}
	content -= margin
	if content <= int64(len(out)) {
		return built{}, fmt.Errorf("sample: insufficient content budget")
	}

	var overhead int64
	for i := 0; i < frames; i++ {
		overhead += b.unitOverhead(int64(float64(content)*float64(weights[i])/float64(totalWeight)), keyFlags[i])
	}
	remaining := content - int64(len(out)) - overhead
	if remaining < int64(frames) {
		return built{}, fmt.Errorf("sample: content budget too small for payloads")
	}

	var res built
	var allocated int64
	for i := 0; i < frames; i++ {
		payload := remaining * weights[i] / totalWeight
		if i == frames-1 {
			payload = remaining - allocated
		}
		allocated += payload
		res.offsets = append(res.offsets, int64(len(out)))
		res.times = append(res.times, int64(i)*frameIntervalMs)
		res.keys = append(res.keys, keyFlags[i])
		unit := b.frameUnit(keyFlags[i], payload)
		res.lengths = append(res.lengths, int64(len(unit)))
		out = append(out, unit...)
	}

	// Pad to the exact target size; shrink the final frame if the remainder
	// is too small to encode as a filler unit.
	rem := b.total - int64(len(out))
	for {
		if unit, ok := b.fillerUnit(rem); ok {
			out = append(out, unit...)
			break
		}
		if len(res.offsets) == 0 {
			return built{}, fmt.Errorf("sample: cannot pad stream")
		}
		last := len(out) - 1
		if out[last] == 0 {
			return built{}, fmt.Errorf("sample: cannot pad stream")
		}
		out = out[:last]
		rem++
		if rem > 4096 {
			return built{}, fmt.Errorf("sample: padding does not converge")
		}
	}
	res.stream = out
	if int64(len(out)) != b.total {
		return built{}, fmt.Errorf("sample: built %d bytes, want %d", len(out), b.total)
	}
	return res, nil
}
