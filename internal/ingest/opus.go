package ingest

import (
	"mediad/internal/model"
)

// opusFrameNS maps the 5-bit Opus TOC config to one frame's duration.
// RFC 6716 Table 1: configs 0-11 -> 20ms, 12-15 -> 10ms, 16-19 -> 40ms,
// 20-23 -> 60ms, 24-29 -> 20ms, 30 -> 10ms; config 31 is reserved/invalid.
var opusFrameNS = [32]model.Nano{
	20 * model.Millisecond, 20 * model.Millisecond, 20 * model.Millisecond, 20 * model.Millisecond, // 0-3
	20 * model.Millisecond, 20 * model.Millisecond, 20 * model.Millisecond, 20 * model.Millisecond, // 4-7
	20 * model.Millisecond, 20 * model.Millisecond, 20 * model.Millisecond, 20 * model.Millisecond, // 8-11
	10 * model.Millisecond, 10 * model.Millisecond, 10 * model.Millisecond, 10 * model.Millisecond, // 12-15
	40 * model.Millisecond, 40 * model.Millisecond, 40 * model.Millisecond, 40 * model.Millisecond, // 16-19
	60 * model.Millisecond, 60 * model.Millisecond, 60 * model.Millisecond, 60 * model.Millisecond, // 20-23
	20 * model.Millisecond, 20 * model.Millisecond, 20 * model.Millisecond, 20 * model.Millisecond, // 24-27
	20 * model.Millisecond, 20 * model.Millisecond, // 28-29
	10 * model.Millisecond, // 30
	0,                      // 31: invalid
}

// parseOpus reads the TOC byte (RFC 6716 section 3.1), validates config and
// frame code against the actual payload, and computes total packet duration.
func parseOpus(spec TrackSpec, pkt *AudioPacket) error {
	const stage = "ingest"
	pl := pkt.Payload
	if len(pl) == 0 {
		return model.ErrOpusTOC.At(spec.ID, uint32(pkt.Sequence), stage,
			pkt.Offset, "missing Opus TOC byte")
	}
	toc := pl[0]
	config := toc >> 3
	if config == 31 {
		return model.ErrOpusTOC.At(spec.ID, uint32(pkt.Sequence), stage,
			pkt.Offset, "reserved Opus TOC config 31")
	}
	code := toc & 0x03
	frames, err := opusFrameCount(code, pl, spec, pkt)
	if err != nil {
		return err
	}
	one := opusFrameNS[config]
	total, err := model.AddNano(0, one*model.Nano(frames))
	if err != nil {
		return model.ErrTimeOverflow.At(spec.ID, uint32(pkt.Sequence), stage,
			pkt.Offset, "opus duration overflow")
	}
	ticks, err := model.RescaleNano(total, spec.ClockRate)
	if err != nil {
		return err
	}
	pkt.DurationNS = total
	pkt.DurationTC = ticks
	return nil
}

// opusFrameCount validates the framing implied by TOC code 0-3 and returns the
// number of encoded frames, rejecting truncated packets with a located error.
func opusFrameCount(code byte, pl []byte, spec TrackSpec, pkt *AudioPacket) (int, error) {
	const stage = "ingest"
	loc := func(msg string) error {
		return model.ErrOpusTOC.At(spec.ID, uint32(pkt.Sequence), stage,
			pkt.Offset, msg)
	}
	switch code {
	case 0: // 1 frame in 1 packet
		if len(pl) < 2 {
			return 0, loc("code 0 packet has no frame data")
		}
		return 1, nil
	case 1: // 2 frames, equal size
		if len(pl) < 3 || (len(pl)-1)%2 != 0 {
			return 0, loc("code 1 packet cannot split into two equal frames")
		}
		return 2, nil
	case 2: // 2 frames, different size
		if len(pl) < 4 {
			return 0, loc("code 2 packet missing frame length byte")
		}
		l := int(pl[1])
		if l == 0 || 2+l >= len(pl) {
			return 0, loc("code 2 frame length exceeds payload")
		}
		return 2, nil
	default: // code 3: arbitrary frame count
		if len(pl) < 3 {
			return 0, loc("code 3 packet missing frame count byte")
		}
		if pl[1]&0x3F == 0 {
			return 0, loc("code 3 frame count must be non-zero")
		}
		return code3Layout(pl, spec, pkt)
	}
}

// code3Layout validates an RFC 6716 code-3 packet and returns its frame count.
func code3Layout(pl []byte, spec TrackSpec, pkt *AudioPacket) (int, error) {
	const stage = "ingest"
	toc := pl[0]
	m := pl[1]
	vbr := toc&0x80 != 0
	hasPadding := toc&0x40 != 0
	frameCount := int(m & 0x3F)
	if vbr {
		frameCount++ // M frames carry lengths, plus one self-delimited frame
	}
	idx := 2
	if hasPadding {
		// Padding length is encoded at the packet tail.
		p := len(pl) - 1
		total := 0
		for p >= idx && pl[p] == 255 {
			total += 254
			p--
		}
		if p < idx {
			return 0, model.ErrOpusTOC.At(spec.ID, uint32(pkt.Sequence),
				stage, pkt.Offset, "code 3 padding runs past frame data")
		}
		total += int(pl[p])
		if total <= 0 || idx+total > len(pl) {
			return 0, model.ErrOpusTOC.At(spec.ID, uint32(pkt.Sequence),
				stage, pkt.Offset, "code 3 padding length exceeds payload")
		}
	}
	if vbr {
		// M one-byte frame lengths precede the frame data.
		idx += frameCount - 1
	}
	if idx >= len(pl) {
		return 0, model.ErrOpusTOC.At(spec.ID, uint32(pkt.Sequence),
			stage, pkt.Offset, "code 3 packet truncated before frame data")
	}
	return frameCount, nil
}
