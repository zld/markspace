// Package audio determines the duration of audio packets (Opus and AAC)
// without decoding their samples. All durations are integer nanoseconds, so no
// floating-point accumulation ever occurs.
package audio

import "mediasync/internal/model"

// Parsed describes one Opus packet's timing without decoding its audio.
type Parsed struct {
	Config     int
	FrameNS    model.NS // duration of a single frame
	Code       int      // TOC packet code: 0..3
	FrameCount int      // number of frames represented
	DurationNS model.NS // total packet duration = FrameNS * FrameCount
	VBR        bool
}

// frameDurationNS maps TOC config 0..31 to its frame duration in nanoseconds.
// Configs 0..11 are SILK-only, 12..15 hybrid, 16..31 CELT-only. Entries
// marked 0 are reserved and rejected.
var frameDurationNS = [32]model.NS{
	10_000_000, 20_000_000, 40_000_000, 60_000_000, // 0..3 SILK
	10_000_000, 20_000_000, 40_000_000, 60_000_000, // 4..7
	10_000_000, 20_000_000, 40_000_000, 60_000_000, // 8..11
	10_000_000, 20_000_000, 40_000_000, 60_000_000, // 12..15 hybrid
	2_500_000, 5_000_000, 10_000_000, 20_000_000, // 16..19 CELT
	2_500_000, 5_000_000, 10_000_000, 20_000_000, // 20..23
	2_500_000, 5_000_000, 10_000_000, 20_000_000, // 24..27
	2_500_000, 5_000_000, 10_000_000, 20_000_000, // 28..31
}

// PacketDuration reads the TOC and (for multi-frame packets) the framing bytes
// and returns the packet duration. packetOffset is the source-track offset,
// echoed into any failure so callers can pinpoint the bad sample.
func PacketDuration(payload []byte, packetOffset int) (Parsed, *model.Failure) {
	if len(payload) == 0 {
		return Parsed{}, model.PtrFailure("", model.StageOpus, packetOffset,
			model.ErrEmptyPacket, "empty Opus payload")
	}
	toc := payload[0]
	config := int(toc >> 3)
	code := int(toc & 0x03)
	frame := frameDurationNS[config]
	if frame == 0 {
		return Parsed{}, model.PtrFailure("", model.StageOpus, packetOffset,
			model.ErrOpusTOC, "reserved/invalid TOC config "+itoa(config))
	}

	p := Parsed{Config: config, FrameNS: frame, Code: code}

	switch code {
	case 0:
		p.FrameCount, p.VBR = 1, false
	case 1:
		p.FrameCount, p.VBR = 2, false
	case 2:
		p.FrameCount, p.VBR = 2, true
	default: // code 3: arbitrary frame count
		if len(payload) < 2 {
			return Parsed{}, model.PtrFailure("", model.StageOpus, packetOffset,
				model.ErrTruncated, "code-3 packet missing frame-count byte")
		}
		m := payload[1]
		p.VBR = m&0x80 != 0
		hasPad := m&0x40 != 0
		p.FrameCount = int(m&0x3F) + 1
		if p.FrameCount > 48 {
			return Parsed{}, model.PtrFailure("", model.StageOpus, packetOffset,
				model.ErrOpusFrameCount, "frame count "+itoa(p.FrameCount)+" exceeds 48")
		}
		if hasPad {
			if f := validatePadding(payload, packetOffset); f != nil {
				return Parsed{}, f
			}
		}
	}

	dur := frame * model.NS(p.FrameCount)
	if dur > model.MaxOpusPacketNS {
		return Parsed{}, model.PtrFailure("", model.StageOpus, packetOffset,
			model.ErrDurationOverflow, "packet duration "+nsToString(dur)+
				" exceeds limit "+nsToString(model.MaxOpusPacketNS))
	}
	p.DurationNS = dur
	return p, nil
}

// validatePadding walks the self-delimiting padding terminator (RFC 6716
// §3.2.1) and reports truncation precisely.
func validatePadding(payload []byte, offset int) *model.Failure {
	idx := len(payload) - 1
	if idx < 2 {
		return model.PtrFailure("", model.StageOpus, offset, model.ErrTruncated,
			"padding marked but packet too short")
	}
	for payload[idx] == 255 {
		idx -= 254
		if idx < 2 {
			return model.PtrFailure("", model.StageOpus, offset, model.ErrTruncated,
				"padding length runs past packet end")
		}
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func nsToString(n model.NS) string {
	var neg bool
	v := int64(n)
	if v < 0 {
		neg, v = true, -v
	}
	var buf [24]byte
	i := len(buf)
	if v == 0 {
		return "0"
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:]) + "ns"
}
