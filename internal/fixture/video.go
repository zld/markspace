// Package fixture generates every sample input used by the selftest and the
// `generate` command: clean multi-bitrate fMP4 renditions, length-framed RTP
// Opus/AAC streams with optional loss/duplication, truncated clips and
// sidecar index material. All bytes are produced in-process and depend only
// on the fixed constants here, so runs are deterministic and no external
// media files are referenced.
package fixture

import (
	gomp4mp4 "replaykit/media/mp4"
)

// Proof timeline constants. Both layers represent the same 30 fps timeline:
// high 90 kHz / 3000-tick frames, low 45 kHz / 1500-tick frames. Each fragment
// carries FramesPerFragment frames and starts with a keyframe.
const (
	HiTimescale       uint32 = 90000
	HiFrameTicks      uint32 = 3000
	LoTimescale       uint32 = 45000
	LoFrameTicks      uint32 = 1500
	FragmentCount     int    = 40
	FramesPerFragment int    = 5
)

// ProofEventsNS are the three highlight trigger times in nanoseconds. They
// sit comfortably inside the ~6.67 s clip and stay disjoint after expansion.
var ProofEventsNS = []int64{
	1_500_000_000,
	3_200_000_000,
	5_000_000_000,
}

// Expansion is the uniform before/after padding in nanoseconds.
const (
	PreNS  int64 = 300_000_000
	PostNS int64 = 400_000_000
)

// buildVideo assembles a deterministic clip with the given timing.
func buildVideo(ts, frame uint32, fragments, perFrag int, payloadSeed byte) *gomp4mp4.Builder {
	b := gomp4mp4.NewBuilder(ts, 1, frame)
	b.WriteInit()
	for f := 0; f < fragments; f++ {
		specs := make([]gomp4mp4.SampleSpec, perFrag)
		base := uint64(f) * uint64(perFrag) * uint64(frame)
		for i := range specs {
			specs[i] = gomp4mp4.SampleSpec{
				Duration: frame,
				Key:      i == 0,
				Payload:  []byte{payloadSeed, byte(f), byte(i), 0x10, 0x20, 0x30, 0x40},
			}
		}
		b.AddFragment(base, uint32(f+1), specs)
	}
	return b
}

// NormalHigh returns the clean 720p rendition bytes.
func NormalHigh() []byte {
	return buildVideo(HiTimescale, HiFrameTicks, FragmentCount, FramesPerFragment, 'H').Bytes()
}

// NormalLow returns the clean 360p rendition bytes on the 45 kHz clock.
func NormalLow() []byte {
	return buildVideo(LoTimescale, LoFrameTicks, FragmentCount, FramesPerFragment, 'L').Bytes()
}

// TruncatedVideo returns a full-timeline clip whose final fragment mdat
// payload is cut by cutBytes, producing exactly one truncated box and one
// dropped fragment while leaving the earlier fragments intact.
func TruncatedVideo(cutBytes int) []byte {
	b := buildVideo(HiTimescale, HiFrameTicks, 20, FramesPerFragment, 'T')
	b.TruncateLast(cutBytes)
	return b.Bytes()
}

// ShortVideo leaves only a few fragments, used for the unrecoverable case.
// extraTruncate optionally cuts into the last mdat.
func ShortVideo(fragments int, extraTruncate int) []byte {
	b := buildVideo(HiTimescale, HiFrameTicks, fragments, FramesPerFragment, 'U')
	if extraTruncate > 0 {
		b.TruncateLast(extraTruncate)
	}
	return b.Bytes()
}

// TamperedSidecar builds a valid sidecar for idx and then changes one sample
// without resealing the checksum, so Verify must fail and force a rebuild.
func TamperedSidecar(idx *gomp4mp4.Index) ([]byte, *gomp4mp4.Sidecar, error) {
	side := gomp4mp4.NewSidecar(idx.Track)
	raw, err := gomp4mp4.MarshalSidecar(side)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := gomp4mp4.ParseSidecar(raw)
	if err != nil {
		return nil, nil, err
	}
	if len(parsed.Samples) > 0 {
		parsed.Samples[0].DecodeTime += 999 // tamper; checksum left stale
	}
	tampered, err := gomp4mp4.MarshalSidecar(parsed)
	if err != nil {
		return nil, nil, err
	}
	return tampered, parsed, nil
}

// NoTimescaleVideo returns ftyp + moov/trak with no mdhd, so no time mapping
// can be established and indexing must fail fatally.
func NoTimescaleVideo() []byte {
	empty := []byte{0, 0, 0, 0}
	moov := wrapFixtureBox("moov", wrapFixtureBox("trak", nil))
	return append(wrapFixtureBox("ftyp", empty), moov...)
}

func wrapFixtureBox(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	out[0] = byte((8 + len(payload)) >> 24)
	out[1] = byte((8 + len(payload)) >> 16)
	out[2] = byte((8 + len(payload)) >> 8)
	out[3] = byte(8 + len(payload))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}
