package container

import (
	"bytes"
	"encoding/binary"
	"math/rand"

	"mediatimeline/internal/clock"
)

// concat joins box payloads without nested byte-slice arithmetic.
func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// FixtureSpec configures a synthetic single-audio-track MP4. Sample payloads
// are deterministic PCM (16-bit stereo) whose per-sample CRC the pipeline can
// verify, so no external media file is ever referenced.
type FixtureSpec struct {
	Seed          int64
	Timescale     uint32 // mdhd timescale, e.g. 44100
	SampleCount   int    // number of samples (each carries FramesPerSample PCM frames)
	FramesPerSamp int    // PCM frames per sample, e.g. 1024
	Channels      uint16
	// Wrap32 makes raw DTS ticks start near the 32-bit boundary so the
	// unwrapper must cross at least one wrap.
	Wrap32 bool
	// DriftPPM perturbs per-sample deltas to emulate a node clock that runs
	// fast (positive) or slow (negative).
	DriftPPM float64
	// VFR jitters the sample duration by +/- this many ticks while preserving
	// the total duration; zero means constant durations.
	VFRJitter int
	// LateIndices marks samples whose container position stays the same but
	// whose timestamps place them before the running watermark when replayed
	// out of order; the generator also emits a separate shuffled ordering.
	LateIndices map[int]bool
}

// box writes an MP4 box header + payload.
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

// fullBox prepends version/flags to a payload.
func fullBox(version byte, payload []byte) []byte {
	out := make([]byte, 4+len(payload))
	out[0] = version
	copy(out[4:], payload)
	return out
}

// pcmSample renders deterministic little-endian 16-bit PCM for one sample.
func pcmSample(rng *rand.Rand, frames, channels int) []byte {
	buf := make([]byte, frames*channels*2)
	for i := 0; i < frames*channels; i++ {
		v := int16(rng.Intn(20000) - 10000)
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	return buf
}

// BuildMP4 renders a complete ftyp+moov+mdat byte stream. It supports a
// truncation mode that cuts the stbl size short (TruncateSTBL) to exercise the
// truncated-box recovery path.
func BuildMP4(spec FixtureSpec) ([]byte, error) {
	if spec.Timescale == 0 || spec.SampleCount == 0 || spec.FramesPerSamp == 0 {
		return nil, clock.NewError(clock.ErrInvariant, "fixture needs timescale, samples and frames")
	}
	rng := rand.New(rand.NewSource(spec.Seed))

	// 1. Payloads and per-sample timing.
	payloads := make([][]byte, spec.SampleCount)
	sizes := make([]uint32, spec.SampleCount)
	deltas := make([]uint32, spec.SampleCount)
	const nominalDelta = 1024
	for i := 0; i < spec.SampleCount; i++ {
		payloads[i] = pcmSample(rng, spec.FramesPerSamp, int(spec.Channels))
		sizes[i] = uint32(len(payloads[i]))
		delta := uint32(nominalDelta)
		if spec.VFRJitter > 0 && i > 0 && i < spec.SampleCount-1 {
			delta = uint32(int(delta) + rng.Intn(2*spec.VFRJitter+1) - spec.VFRJitter)
		}
		if spec.DriftPPM != 0 {
			scaled := int64(float64(delta) * (1 + spec.DriftPPM/1e6))
			if scaled < 1 {
				scaled = 1
			}
			delta = uint32(scaled)
		}
		deltas[i] = delta
	}

	// 2. stbl child boxes.
	stts := encodeStts(deltas)
	stsz := encodeStsz(sizes)
	stsc := box("stsc", fullBox(0, encodeStsc()))
	// One chunk per sample keeps offset math trivial and VFR-friendly.
	co := make([]uint64, spec.SampleCount)
	stco := box("stco", fullBox(0, encodeStco(co))) // offsets patched after layout
	stblBody := bytes.NewBuffer(nil)
	for _, p := range [][]byte{
		box("stsd", fullBox(0, encodeStsd(spec.Channels, spec.Timescale))),
		stts, stsc, stsz, stco,
	} {
		stblBody.Write(p)
	}

	// 3. Track and movie metadata.
	mdhd := box("mdhd", fullBox(0, encodeMdhd(spec.Timescale, deltas)))
	hdlr := box("hdlr", fullBox(0, encodeHdlr("soun")))
	minf := box("minf", concat(
		box("smhd", fullBox(0, nil)),
		box("dinf", box("dref", fullBox(0, encodeDref()))),
		box("stbl", stblBody.Bytes())))
	mdia := box("mdia", concat(mdhd, hdlr, minf))
	tkhd := box("tkhd", fullBox(0, encodeTkhd(1)))
	trak := box("trak", concat(tkhd, mdia))
	mvhd := box("mvhd", fullBox(0, encodeMvhd(spec.Timescale, deltas)))
	moov := box("moov", concat(mvhd, trak))
	ftyp := box("ftyp", []byte("isom"+"\x00\x00\x02\x00"+"isomiso4"))

	// 4. Layout: ftyp, moov, then mdat; patch stco with real offsets.
	mdatPayload := bytes.NewBuffer(nil)
	for _, p := range payloads {
		mdatPayload.Write(p)
	}
	mdat := box("mdat", mdatPayload.Bytes())
	mdatDataOffset := int64(len(ftyp) + len(moov) + 8)
	for i := range co {
		co[i] = uint64(mdatDataOffset)
		mdatDataOffset += int64(sizes[i])
	}
	patchedStco := box("stco", fullBox(0, encodeStco(co)))
	moov = bytesReplaceBox(moov, "stco", patchedStco)

	out := bytes.NewBuffer(nil)
	out.Write(ftyp)
	out.Write(moov)
	out.Write(mdat)
	return out.Bytes(), nil
}

// bytesReplaceBox swaps the first occurrence of a child box type inside a
// serialized container, rebuilding every parent box whose size changes. The
// walk operates on complete boxes (header included) at every level.
func bytesReplaceBox(parent []byte, typ string, replacement []byte) []byte {
	var walk func(region []byte) []byte
	walk = func(region []byte) []byte {
		res := bytes.NewBuffer(nil)
		for off := 0; off < len(region); {
			if off+8 > len(region) {
				res.Write(region[off:])
				break
			}
			size := int(binary.BigEndian.Uint32(region[off:]))
			if size < 8 || off+size > len(region) {
				res.Write(region[off:])
				break
			}
			whole := region[off : off+size]
			if string(whole[4:8]) == typ {
				res.Write(replacement)
			} else {
				inner := walk(whole[8:])
				hdr := make([]byte, 8)
				binary.BigEndian.PutUint32(hdr, uint32(8+len(inner)))
				copy(hdr[4:8], whole[4:8])
				res.Write(hdr)
				res.Write(inner)
			}
			off += size
		}
		return res.Bytes()
	}
	return walk(parent)
}
