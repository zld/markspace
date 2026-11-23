package pipeline

import (
	"mediaroute/internal/clock"
	"mediaroute/internal/codec"
	"mediaroute/internal/transport"
)

func keyframeChecker(c codec.Codec) func(*transport.Frame) (bool, error) {
	return func(f *transport.Frame) (bool, error) {
		return inspectFrame(c, f.Bytes)
	}
}

func inspectFrame(c codec.Codec, data []byte) (bool, error) {
	switch c {
	case codec.H264, codec.H265:
		return codec.FrameKey(c, data)
	case codec.AV1:
		return codec.FrameKeyAV1(data)
	default:
		return false, clock.E(clock.PhaseCodec, 0, -1, false,
			"unsupported codec marker: "+string(c), nil)
	}
}

// corruptStreamBytes deliberately destroys a frame's structural boundary so
// the codec parser rejects it with a non-recoverable error. For AV1 it clears
// the mandatory OBU size field bit; for Annex B it flips the start code into
// an invalid forbidden prefix the NAL splitter cannot resync to.
func corruptStreamBytes(c codec.Codec, data []byte) []byte {
	out := append([]byte(nil), data...)
	switch c {
	case codec.AV1:
		if len(out) >= 2 {
			out[0] = out[0] &^ 0x02 // clear obu_has_size_field -> unsupported layout
			out[1] = 0xFF
		}
	default:
		// Flip the forbidden_zero_bit of the first NAL header (the byte right
		// after its 3/4-byte start code) so the codec stage rejects the frame.
		if sc := nalStartLen(out); sc > 0 && sc < len(out) {
			out[sc] = out[sc] | 0x80
		}
	}
	return out
}

// nalStartLen returns 4 or 3 for a leading Annex B start code, else 0.
func nalStartLen(data []byte) int {
	if len(data) >= 4 && data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		return 4
	}
	if len(data) >= 3 && data[0] == 0 && data[1] == 0 && data[2] == 1 {
		return 3
	}
	return 0
}

// safeTruncate reduces a damaged frame to a parseable prefix: whole NAL units
// for Annex B codecs, whole OBUs for AV1. It never fabricates a keyframe.
func safeTruncate(c codec.Codec, data []byte) []byte {
	switch c {
	case codec.H264, codec.H265:
		// Keep bytes up to the last complete start-code-delimited unit.
		units := codec.SplitAnnexB(data)
		if len(units) == 0 {
			return data[:0]
		}
		last := units[len(units)-1]
		return data[:last.Offset]
	case codec.AV1:
		return codec.TruncateAV1(data)
	default:
		return data
	}
}
