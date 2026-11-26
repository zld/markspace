package fixture

import (
	"fmt"
	"strconv"

	"regress/internal/scan"
)

// TranscodeArgs builds the codec-correct output arguments for re-encoding an
// elementary stream at a given candidate geometry/bitrate/GOP. Centralizing
// this keeps the worker (trial encode) and publisher (fragmented encode) on
// identical encoder settings.
//
// SVT-AV1 only supports CRF-based rate control with maxrate in VBR mode, so
// bitrate caps are expressed via -maxrate while CRF is derived from the rung;
// H.264/H.265 use standard capped VBR with bufsize.
func TranscodeArgs(codec string, width, height, gop int, bitrateBPS int64, crf int) []string {
	common := []string{
		"-c:v", EncoderLib(codec),
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-g", strconv.Itoa(gop),
		"-pix_fmt", "yuv420p",
	}
	switch codec {
	case scan.CodecAV1:
		// -g sets the keyframe interval and keyint inside svtav1-params
		// reinforces it so fragments align exactly with the keyframe table.
		// rc=0 selects CRF mode, which is required for maxrate support.
		return append(common,
			"-crf", strconv.Itoa(crf),
			"-maxrate", strconv.FormatInt(bitrateBPS, 10),
			"-bufsize", strconv.FormatInt(bitrateBPS*2, 10),
			"-svtav1-params",
			fmt.Sprintf("pred-struct=2:rc=0:enable-tf=0:keyint=%d", gop),
		)
	case scan.CodecH265:
		return append(common,
			"-b:v", strconv.FormatInt(bitrateBPS, 10),
			"-maxrate", strconv.FormatInt(bitrateBPS, 10),
			"-bufsize", strconv.FormatInt(bitrateBPS*2, 10),
			"-x265-params", "bframes=0:aud=1:repeat-headers=1",
		)
	default: // H.264
		return append(common,
			"-b:v", strconv.FormatInt(bitrateBPS, 10),
			"-maxrate", strconv.FormatInt(bitrateBPS, 10),
			"-bufsize", strconv.FormatInt(bitrateBPS*2, 10),
			"-bf", "0", "-sc_threshold", "0",
		)
	}
}

// EncoderLib maps a codec identifier to its FFmpeg encoder name.
func EncoderLib(codec string) string {
	switch codec {
	case scan.CodecH265:
		return "libx265"
	case scan.CodecAV1:
		return "libsvtav1"
	default:
		return "libx264"
	}
}

// RawMuxer maps a codec to its raw bitstream muxer name.
func RawMuxer(codec string) string {
	switch codec {
	case scan.CodecH265:
		return "hevc"
	case scan.CodecAV1:
		return "obu"
	default:
		return codec
	}
}
