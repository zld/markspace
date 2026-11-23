package pipeline

import (
	"encoding/binary"
	"math"
	"sort"

	"mediacore/ingest"
	"mediacore/media"
)

const (
	videoSSRC     = 0x01020304
	audioSSRC     = 0x05060708
	fragsPerFrame = 16   // sequence numbers are derived as frame*16+frag
	maxConceal    = 16   // loss bursts up to this many packets are concealed
	videoTicks    = 3000 // 90000 Hz / 30 fps
)

type vframe struct{ off, end, vcl int64 }

func isFrameUnit(u ingest.Unit, c ingest.Codec) bool {
	switch c {
	case ingest.CodecH264:
		return u.Type >= 1 && u.Type <= 5
	case ingest.CodecH265:
		return u.Type <= 31
	default: // av1: frame header / frame OBU
		return u.Type == 3 || u.Type == 6
	}
}

// groupFrames folds scanned units into frames: non-frame units (SPS/PPS,
// sequence headers) attach as a prefix to the next frame unit.
func groupFrames(units []ingest.Unit, c ingest.Codec) []vframe {
	var frames []vframe
	prefix := int64(-1)
	for _, u := range units {
		if isFrameUnit(u, c) {
			start := u.Offset
			if prefix >= 0 {
				start = prefix
			}
			frames = append(frames, vframe{off: start, end: u.Offset + u.Size, vcl: u.Offset})
			prefix = -1
		} else if prefix < 0 {
			prefix = u.Offset
		}
	}
	return frames
}

// rebuildIndex derives a keyframe index from the bitstream itself. Used
// to recover after a declared index is rejected and quarantined.
func rebuildIndex(stream []byte, c ingest.Codec) (*ingest.KeyframeIndex, error) {
	units, err := ingest.Scan(stream, c)
	if err != nil {
		return nil, err
	}
	idx := &ingest.KeyframeIndex{Digest: ingest.DigestOf(stream)}
	for _, u := range units {
		if u.Keyframe {
			idx.Offsets = append(idx.Offsets, u.Offset)
			idx.Sizes = append(idx.Sizes, u.Size)
		}
	}
	return idx, nil
}

func f32bytes(s []float32) []byte {
	b := make([]byte, 4*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	return b
}

func bytesF32(b []byte) []float32 {
	s := make([]float32, len(b)/4)
	for i := range s {
		s[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return s
}

func p95(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(0.95*float64(len(s)-1))]
}

// run is the staged execution behind Run. Filled in incrementally:
// stages are ingest -> audio -> per-frame rtp/output, checkpointed at
// GOP boundaries.
func (r *Runner) run() (Result, error) {
	return Result{}, media.Fault(media.CodeInternal, "pipeline.run", -1, "not implemented")
}
