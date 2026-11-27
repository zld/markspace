package synthetic

import "encoding/binary"

// AudioFragment builds an audio CMAF fragment whose mdat is one PCM s16le
// mono payload at 48kHz. The deterministic sine-derived samples keep peak and
// RMS levels reproducible across runs with the same seed.
func AudioFragment(trackID, sequence uint32, baseDecodeTime uint64, timescale uint64,
	sampleCount int, seed byte) []byte {
	pcm := make([]byte, sampleCount*2)
	for i := 0; i < sampleCount; i++ {
		// Triangle-ish deterministic waveform bounded well below full scale.
		phase := (i + int(seed)) % 48
		v := int16(0)
		switch {
		case phase < 12:
			v = int16(phase) * 200
		case phase < 24:
			v = int16(24-phase) * 200
		case phase < 36:
			v = -int16(phase-24) * 200
		default:
			v = -int16(48-phase) * 200
		}
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	// One trun entry covering the whole PCM payload at the given timescale.
	dur := uint32(uint64(sampleCount) * timescale / 48000)
	return buildRawFragment(trackID, sequence, baseDecodeTime, [][]byte{pcm},
		[]uint32{dur}, nil)
}

// buildRawFragment wraps prebuilt sample payloads with styp/moof/mdat boxes.
// cts may be nil; otherwise it must align 1:1 with payloads.
func buildRawFragment(trackID, sequence uint32, baseDecodeTime uint64,
	payloads [][]byte, durations []uint32, cts []int32) []byte {
	var b boxBuilder
	ps := b.start("styp")
	b.raw([]byte("msdh"))
	writeU32(&b.buf, 0)
	b.raw([]byte("msdh"))
	b.raw([]byte("msix"))
	b.end(ps)

	moofStart := b.start("moof")
	mf := b.start("mfhd")
	b.raw(fullHeader(0, 0))
	writeU32(&b.buf, sequence)
	b.end(mf)

	traf := b.start("traf")
	tfhd := b.start("tfhd")
	b.raw(fullHeader(0, 0x020000)) // default-base-is-moof
	writeU32(&b.buf, trackID)
	b.end(tfhd)
	tfdt := b.start("tfdt")
	b.raw(fullHeader(1, 0))
	writeU64(&b.buf, baseDecodeTime)
	b.end(tfdt)
	trun := b.start("trun")
	flags := uint32(0x000301) // data-offset | duration | size
	if cts != nil {
		flags |= 0x000800
	}
	b.raw(fullHeader(1, flags))
	writeU32(&b.buf, uint32(len(payloads)))
	dataOffPos := b.buf.Len()
	writeU32(&b.buf, 0)
	for i, p := range payloads {
		writeU32(&b.buf, durations[i])
		writeU32(&b.buf, uint32(len(p)))
		if cts != nil {
			writeI32(&b.buf, cts[i])
		}
	}
	b.end(trun)
	b.end(traf)
	b.end(moofStart)

	mdat := b.start("mdat")
	payloadStart := b.buf.Len()
	for _, p := range payloads {
		b.raw(p)
	}
	b.end(mdat)
	dataOffset := uint32(payloadStart - (moofStart - 8))
	binary.BigEndian.PutUint32(b.buf.Bytes()[dataOffPos:], dataOffset)
	return b.buf.Bytes()
}
