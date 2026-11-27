// Package fixture generates fully self-contained sample assets: valid raw
// H.264/H.265/AV1 bitstreams with matching key-frame indexes, pixel frames for
// quality measurement, and a family of deliberately corrupted variants. No
// external files are referenced; every byte is derived from the seed.
package fixture

import (
	"math/rand"

	"mediad/internal/ingest"
	"mediad/internal/model"
)

// Sample is one generated asset plus everything the pipeline needs to process
// and measure it.
type Sample struct {
	Codec         string
	Width         uint16
	Height        uint16
	FPS           model.Rational
	KeyEvery      int
	TargetBPS     int64
	Stream        []byte
	Index         *ingest.Index
	Frames        [][]byte // original pixel frames, each Width*Height bytes
	DerivedFrames [][]byte // near-lossless quantized frames
	DurationNS    int64
	Seed          int64
}

// Params controls sample generation.
type Params struct {
	Codec     string
	Width     uint16
	Height    uint16
	FPS       model.Rational
	Frames    int
	KeyEvery  int
	TargetBPS int64
	Seed      int64
}

// DefaultParams returns sensible per-codec generation parameters.
func DefaultParams(codec string, seed int64) Params {
	return Params{
		Codec:     codec,
		Width:     176,
		Height:    144,
		FPS:       model.Rational{Num: 30, Den: 1},
		Frames:    30,
		KeyEvery:  10,
		TargetBPS: 400_000,
		Seed:      seed,
	}
}

// appendStartCode writes a 4-byte Annex-B start code.
func appendStartCode(b []byte) []byte { return append(b, 0, 0, 0, 1) }

// generateFrames builds deterministic original pixel frames and near-lossless
// derived frames. Quantization to a multiple of 4 bounds per-pixel error to
// ±2, which keeps PSNR high and SSIM essentially 1.
func generateFrames(rng *rand.Rand, w, h, count int) (orig, derived [][]byte) {
	orig = make([][]byte, count)
	derived = make([][]byte, count)
	n := w * h
	for f := 0; f < count; f++ {
		src := make([]byte, n)
		der := make([]byte, n)
		for i := 0; i < n; i++ {
			// Spatial gradient plus slowly varying texture, all seed-derived.
			v := (byte(i%251) + byte(f*7) + byte(rng.Intn(8)))
			src[i] = v
			der[i] = byte((int(v) / 4) * 4) // quantize; |error| <= 2
		}
		orig[f] = src
		derived[f] = der
	}
	return orig, derived
}

// Generate builds a valid sample: codec-framed stream, matching index, pixel
// frames and a payload tuned to the target bitrate.
func Generate(p Params) (*Sample, error) {
	if !ingest.IsSupportedCodec(p.Codec) {
		return nil, model.New(model.ErrUnknownCodec, p.Codec, -1, "fixture codec unsupported")
	}
	if p.Frames <= 0 || p.KeyEvery <= 0 {
		return nil, model.New(model.ErrStageState, p.Codec, -1, "fixture needs positive frame/key counts")
	}
	rng := rand.New(rand.NewSource(p.Seed))
	w, h := int(p.Width), int(p.Height)
	orig, derived := generateFrames(rng, w, h, p.Frames)

	// Exact integer duration and target byte budget.
	durationNS, err := durationNS(p.Frames, p.FPS)
	if err != nil {
		return nil, err
	}
	targetBytes, err := bytesForBitrate(p.TargetBPS, durationNS)
	if err != nil {
		return nil, err
	}

	s := &Sample{
		Codec:         p.Codec,
		Width:         p.Width,
		Height:        p.Height,
		FPS:           p.FPS,
		KeyEvery:      p.KeyEvery,
		TargetBPS:     p.TargetBPS,
		Frames:        orig,
		DerivedFrames: derived,
		DurationNS:    durationNS,
		Seed:          p.Seed,
	}

	switch p.Codec {
	case ingest.CodecH264, ingest.CodecH265:
		s.Stream, s.Index, err = buildAnnexBSample(p, rng, targetBytes)
	case ingest.CodecAV1:
		s.Stream, s.Index, err = buildAV1Sample(p, rng, targetBytes)
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// durationNS computes the exact presentation duration of frameCount frames.
func durationNS(frameCount int, fps model.Rational) (int64, error) {
	return model.FramePTSNS(int64(frameCount), fps)
}

// bytesForBitrate converts a target bitrate and duration into a byte budget
// using integer math with a single rounding step.
func bytesForBitrate(targetBPS, durationNS int64) (int64, error) {
	bits, err := model.CheckedMul(targetBPS, durationNS)
	if err != nil {
		return 0, err
	}
	bytesTimes8, err := model.RoundDiv(bits, model.NanoPerSec)
	if err != nil {
		return 0, err
	}
	return model.RoundDiv(bytesTimes8, 8)
}

// buildAnnexBSample constructs H.264/H.265 NAL units and key-frame index.
// Filler needed to reach targetBytes is appended to the last NAL unit's
// payload (the trailing NAL has no following start code, so the bytes are
// simply part of its RBSP) and the final index entry is extended.
func buildAnnexBSample(p Params, rng *rand.Rand, targetBytes int64) ([]byte, *ingest.Index, error) {
	stream := make([]byte, 0, 4096)
	entries := make([]ingest.IndexEntry, 0)

	// Parameter-set units first (SPS/PPS for H.264; VPS/SPS/PPS for H.265).
	if p.Codec == ingest.CodecH264 {
		stream = appendStartCode(stream)
		stream = append(stream, 0x67) // SPS, ref_idc=3, type=7
		stream = append(stream, byte(p.Width), byte(p.Height))
		stream = appendStartCode(stream)
		stream = append(stream, 0x68) // PPS, type=8
		stream = append(stream, 0xEE)
	} else {
		stream = appendStartCode(stream)
		stream = append(stream, 0x40, 0x01) // VPS type 32
		stream = appendStartCode(stream)
		stream = append(stream, 0x42, 0x01) // SPS type 33
		stream = appendStartCode(stream)
		stream = append(stream, 0x44, 0x01) // PPS type 34
	}

	for f := 0; f < p.Frames; f++ {
		isKey := f%p.KeyEvery == 0
		off := int64(len(stream))
		stream = appendStartCode(stream)
		var hdr []byte
		if p.Codec == ingest.CodecH264 {
			nalType := byte(1) // non-IDR slice
			if isKey {
				nalType = 5 // IDR
			}
			hdr = []byte{0x60 | nalType}
		} else {
			nalType := byte(1) // TRAIL_N
			if isKey {
				nalType = 19 // IDR_W_RADL
			}
			hdr = []byte{(nalType << 1) & 0x7e, 0x01}
		}
		stream = append(stream, hdr...)
		// Deterministic per-frame payload: frame marker and texture bytes.
		stream = append(stream, byte(f>>8), byte(f))
		payload := 16 + rng.Intn(16)
		for i := 0; i < payload; i++ {
			stream = append(stream, byte(rng.Intn(256)))
		}
		if isKey {
			entries = append(entries, ingest.IndexEntry{
				Offset: off,
				Length: int64(len(stream)) - off,
				Key:    true,
			})
		}
	}
	// Absorb the bitrate filler into the final NAL unit's trailing bytes.
	// No start code follows, so the scanner keeps them inside the last unit.
	if int64(len(stream)) < targetBytes {
		stream = append(stream, make([]byte, targetBytes-int64(len(stream)))...)
		if len(entries) > 0 {
			last := &entries[len(entries)-1]
			last.Length = int64(len(stream)) - last.Offset
		}
	}
	idx, err := ingest.ValidateIndex(p.Codec, entries, int64(len(stream)))
	if err != nil {
		return nil, nil, err
	}
	return stream, idx, nil
}

// buildAV1Sample constructs low-overhead AV1 OBUs and a key-frame index.
// buildAV1Sample constructs low-overhead AV1 OBUs and a key-frame index.
// Filler needed to reach targetBytes is added to the last frame OBU's declared
// payload so the entire stream remains a sequence of well-formed OBUs.
func buildAV1Sample(p Params, rng *rand.Rand, targetBytes int64) ([]byte, *ingest.Index, error) {
	stream := make([]byte, 0, 4096)
	entries := make([]ingest.IndexEntry, 0)

	// Temporal delimiter (type 2), empty payload.
	stream = appendAV1OBU(stream, 2, nil)
	// Sequence header (type 1) with minimal config bytes.
	stream = appendAV1OBU(stream, 1, []byte{byte(p.Width), byte(p.Height), byte(p.FPS.Num), byte(p.FPS.Den)})

	// Pre-build frame OBU payloads so the final one can absorb the filler.
	framePayloads := make([][]byte, p.Frames)
	for f := 0; f < p.Frames; f++ {
		isKey := f%p.KeyEvery == 0
		var first byte
		if !isKey {
			first = 0x40 // frame_type = INTER (0b01)
		}
		payload := []byte{first, byte(f >> 8), byte(f)}
		for i := 0; i < 8+rng.Intn(8); i++ {
			payload = append(payload, byte(rng.Intn(256)))
		}
		framePayloads[f] = payload
	}

	// Measure the exact byte length the frame OBUs would occupy, then size the
	// filler so the total reaches targetBytes. LEB128 width is measured
	// explicitly because a large filler can widen the size field.
	baseLen := int64(len(stream))
	measure := func(payloads [][]byte) int64 {
		var n int64
		for _, pl := range payloads {
			n += 1 + int64(len(appendLeb128(nil, len(pl)))) + int64(len(pl))
		}
		return n
	}
	if cur := baseLen + measure(framePayloads); cur < targetBytes {
		last := len(framePayloads) - 1
		framePayloads[last] = append(framePayloads[last], make([]byte, targetBytes-cur)...)
	}

	for f, pl := range framePayloads {
		off := int64(len(stream))
		stream = appendAV1OBU(stream, 3, pl)
		if f%p.KeyEvery == 0 {
			entries = append(entries, ingest.IndexEntry{
				Offset: off,
				Length: int64(len(stream)) - off,
				Key:    true,
			})
		}
	}
	idx, err := ingest.ValidateIndex(p.Codec, entries, int64(len(stream)))
	if err != nil {
		return nil, nil, err
	}
	return stream, idx, nil
}

// appendAV1OBU writes one OBU with header, LEB128 size and payload.
func appendAV1OBU(b []byte, obuType byte, payload []byte) []byte {
	hdr := (obuType << 3) | 0x02 // obu_has_size_flag=1
	b = append(b, hdr)
	b = appendLeb128(b, len(payload))
	b = append(b, payload...)
	return b
}

// appendLeb128 encodes n as unsigned LEB128: every byte but the last has its
// high continuation bit set.
func appendLeb128(b []byte, n int) []byte {
	var out []byte
	for {
		c := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			out = append(out, c|0x80)
		} else {
			out = append(out, c)
			break
		}
	}
	return append(b, out...)
}
