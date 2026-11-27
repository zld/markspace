package ingest

import (
	"fmt"

	"continuity/internal/timeline"
)

// OpusSampleRate is the fixed Opus media clock rate (RFC 7845).
const OpusSampleRate = 48000

// opusFrameSamples maps the 5-bit TOC config to per-frame samples at 48 kHz.
// SILK (0-11): 10/20/40/60 ms; HYBRID (12-15): 10/20 ms;
// CELT (16-31): 2.5/5/10/20 ms.
func opusFrameSamples(config byte) int {
	switch {
	case config < 12:
		return []int{480, 960, 1920, 2880}[config%4]
	case config < 16:
		return []int{480, 960}[config%2]
	default:
		return []int{120, 240, 480, 960}[config%4]
	}
}

// OpusPacketSamples returns the total number of samples in an Opus packet
// from its TOC header alone (RFC 6716 §3.1). Unknown or truncated packets
// yield a structured *timeline.Fault naming track, stage and offset.
func OpusPacketSamples(track string, offset int64, payload []byte) (int, error) {
	fault := func(msg string) (int, error) {
		return 0, &timeline.Fault{
			Track: track, Stage: timeline.StageIngest, Offset: offset, Msg: msg,
		}
	}
	if len(payload) < 1 {
		return fault("truncated Opus packet: missing TOC byte")
	}
	toc := payload[0]
	frame := opusFrameSamples(toc >> 3)
	switch toc & 0x03 {
	case 0: // one frame
		return frame, nil
	case 1, 2: // two frames, equal or different compressed sizes, same duration
		return 2 * frame, nil
	case 3: // arbitrary frame count in the second byte
		if len(payload) < 2 {
			return fault("truncated Opus packet: missing frame-count byte")
		}
		n := int(payload[1] & 0x3F)
		if n < 1 || frame*n > 5760 {
			return fault(fmt.Sprintf("invalid Opus frame count %d for %d-sample frames", n, frame))
		}
		return n * frame, nil
	}
	return fault("unreachable Opus frame-count code")
}

// AACFrameSamples is the number of samples per AAC-LC access unit.
const AACFrameSamples = 1024

// AACFrameDuration converts one AAC frame to ticks on the MP4 track clock:
// 1024 * timescale / sampleRate, computed in integers.
func AACFrameDuration(clk timeline.Clock, sampleRate int64) (timeline.Ticks, error) {
	if sampleRate <= 0 {
		return 0, &timeline.Fault{
			Track: "audio/mp4", Stage: timeline.StageScan, Offset: -1,
			Msg: fmt.Sprintf("invalid AAC sample rate %d", sampleRate),
		}
	}
	return (timeline.Clock{Timescale: sampleRate}).Convert(AACFrameSamples, clk)
}
