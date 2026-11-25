package gen

import "encoding/binary"

// PCM16 builds little-endian signed 16-bit PCM from normalised [-1,1] values.
func PCM16(levels []float64) []byte {
	b := make([]byte, len(levels)*2)
	for i, v := range levels {
		x := int16(clamp(v, -1, 1) * 32767)
		binary.LittleEndian.PutUint16(b[i*2:], uint16(x))
	}
	return b
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Audio timescale used by all generated audio tracks.
const AudioTimescale uint32 = 48000

// NormalGroup builds a healthy multi-fragment audio group:
//   - fragment 1: loud samples,
//   - fragment 2: a loud block then a long zero-amplitude silent block,
//   - fragment 3: identical loud content to fragment 1 (a duplicate).
// Each sample lasts 1024 ticks (~21.3ms at 48kHz).
func NormalGroup() []byte {
	loud := PCM16([]float64{0.5, -0.5, 0.4, -0.4})
	quiet := PCM16([]float64{0.0, 0.0, 0.0, 0.0})
	dur := uint32(1024)

	f1 := Fragment(1, []TrackSpec{{
		TrackID: 1, BaseT: 0,
		Samples: []SampleSpec{{PCM: loud, Duration: dur}},
	}})
	f2 := Fragment(2, []TrackSpec{{
		TrackID: 1, BaseT: 1024,
		Samples: []SampleSpec{
			{PCM: loud, Duration: dur},
			{PCM: quiet, Duration: dur},
			{PCM: quiet, Duration: dur},
		},
	}})
	// Same content and base sequence as f1's payload but a later sequence
	// number; the content fingerprint still equals fragment 1's.
	f3 := Fragment(3, []TrackSpec{{
		TrackID: 1, BaseT: 4096,
		Samples: []SampleSpec{{PCM: loud, Duration: dur}},
	}})
	return append(append(append([]byte{}, f1...), f2...), f3...)
}

// BoundaryGroup exercises RTP/tfdt wrap handling and variable frame rates:
//   - fragment 1 sits at the high end of the 32-bit tick window,
//   - fragment 2 wraps tfdt back near zero (valid rollover),
//   - fragment 3 performs an unexplained mid-window tfdt rollback (invalid),
//   - video samples use uneven durations (VFR, never a fixed rate).
func BoundaryGroup() []byte {
	// 90kHz video timescale, VFR durations.
	videoTS := uint32(90000)
	_ = videoTS
	payloadA := PCM16([]float64{0.2})
	payloadB := PCM16([]float64{0.3})
	payloadC := PCM16([]float64{0.4})

	high := uint32(0xFFFFFFF0)
	f1 := Fragment(10, []TrackSpec{{
		TrackID: 2, BaseT: high,
		Samples: []SampleSpec{
			{PCM: payloadA, Duration: 8},   // short VFR frame
			{PCM: payloadA, Duration: 17},  // longer VFR frame (wraps)
		},
	}})
	// tfdt wrapped to a small value: a legitimate rollover continuation.
	f2 := Fragment(11, []TrackSpec{{
		TrackID: 2, BaseT: 15,
		Samples: []SampleSpec{
			{PCM: payloadB, Duration: 5},
			{PCM: payloadB, Duration: 33}, // VFR again
		},
	}})
	// After the rollover the raw clock sits low (fragment 2 base 15). A
	// third fragment whose tfdt drops to 5 is now a rollback on the unwrapped
	// absolute axis (both are in the bottom half, so no second rollover is
	// plausible). The timeline must flag this interval invalid rather than
	// fabricate a time for it.
	f3 := Fragment(12, []TrackSpec{{
		TrackID: 2, BaseT: 5,
		Samples: []SampleSpec{
			{PCM: payloadC, Duration: 9},
		},
	}})
	return append(append(append([]byte{}, f1...), f2...), f3...)
}

// TruncatedGroup returns a healthy fragment plus a second fragment whose mdat
// is physically cut. cutOffset is set to the absolute byte position of the
// truncation (the first missing byte), which the parser must locate ±1 byte.
func TruncatedGroup() (data []byte, cutOffset int64) {
	loud := PCM16([]float64{0.5, -0.5})
	f1 := Fragment(1, []TrackSpec{{
		TrackID: 1, BaseT: 0,
		Samples: []SampleSpec{{PCM: loud, Duration: 1024}},
	}})
	// A fragment with an mdat larger than the bytes we keep.
	f2 := Fragment(2, []TrackSpec{{
		TrackID: 1, BaseT: 1024,
		Samples: []SampleSpec{
			{PCM: make([]byte, 64), Duration: 1024},
		},
	}})
	// Keep the full moof and mdat header but cut 40 bytes into the mdat.
	mdatHeaderLen := 8
	keep := len(f2) - 40
	cutOffset = int64(len(f1) + keep)
	truncated := f2[:keep]
	_ = mdatHeaderLen
	return append(append([]byte{}, f1...), truncated...), cutOffset
}
