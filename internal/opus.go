package rtpreplay

import "fmt"

// Codec identifies a media codec supported by the offline replay.
type Codec string

const (
	CodecOpus Codec = "opus"
	CodecAAC  Codec = "aac"
	CodecNone Codec = ""
)

// opusFrameSamples48k maps an Opus TOC configuration (0-31) to the number of
// samples one frame occupies at the fixed 48 kHz Opus RTP clock. Durations:
// SILK 10/20/40/60 ms, Hybrid 10/20 ms, CELT 2.5/5/10/20 ms.
var opusFrameSamples48k = [32]uint32{
	480, 960, 1920, 2880, // 0-3   SILK NB
	480, 960, 1920, 2880, // 4-7   SILK MB
	480, 960, 1920, 2880, // 8-11  SILK WB
	480, 960, 480, 960, // 12-15 Hybrid SWB/FB
	120, 240, 480, 960, // 16-19 CELT NB
	120, 240, 480, 960, // 20-23 CELT WB
	120, 240, 480, 960, // 24-27 CELT SWB
	120, 240, 480, 960, // 28-31 CELT FB
}

// OpusTOC is the parsed table-of-contents byte that starts every Opus packet.
// Duration is derivable from this byte alone, so no payload decoding is
// required for timeline reconstruction.
type OpusTOC struct {
	Config       uint8
	Stereo       bool
	FrameCode    uint8
	Frames       int
	FrameSamples uint32 // per frame, at 48 kHz
}

// ParseOpusTOC parses the first payload byte of an Opus packet.
func ParseOpusTOC(payload []byte) (OpusTOC, error) {
	if len(payload) == 0 {
		return OpusTOC{}, NewError(CodeDuration, "opus_toc", "empty Opus payload: TOC byte missing")
	}
	b := payload[0]
	toc := OpusTOC{
		Config:    b >> 3,
		Stereo:    b&0x04 != 0,
		FrameCode: b & 0x03,
	}
	toc.FrameSamples = opusFrameSamples48k[toc.Config]
	switch toc.FrameCode {
	case 0:
		toc.Frames = 1
	case 1, 2:
		toc.Frames = 2
	case 3:
		toc.Frames = 3
	}
	if toc.FrameSamples == 0 {
		return OpusTOC{}, NewError(CodeDuration, "opus_toc", fmt.Sprintf("invalid TOC config %d", toc.Config))
	}
	return toc, nil
}

// DurationTicks returns the total packet duration expressed in ticks of the
// given RTP clock rate. The conversion is integer-exact; a non-integral
// result or a 64-bit overflow is reported as a structured error rather than
// silently truncated.
func (toc OpusTOC) DurationTicks(clockRate uint32) (uint64, error) {
	if clockRate == 0 {
		return 0, NewError(CodeInvalidConfig, "opus_duration", "zero clock rate")
	}
	samples := uint64(toc.FrameSamples) * uint64(toc.Frames)
	if clockRate == 48000 {
		return samples, nil
	}
	ticks, err := Scale(samples, 48000, clockRate)
	if err != nil {
		return 0, err
	}
	// Verify exactness: scaling down must be lossless for the timeline.
	back, err := Scale(ticks, clockRate, 48000)
	if err != nil {
		return 0, err
	}
	if back != samples {
		return 0, NewError(CodeDuration, "opus_duration",
			fmt.Sprintf("duration of %d samples at 48kHz is not integral at %dHz", samples, clockRate))
	}
	return ticks, nil
}
