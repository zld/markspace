// Package rtp incrementally parses a length-framed RTP/RTCP byte stream.
// Frames are prefixed with a two-byte big-endian length (RFC 4571 style).
// Corrupt frames are classified, recorded with their byte offset and, when
// the fixed header survived, their sequence number, then the scanner
// resynchronizes on the next plausible frame boundary.
//
// Only the payload types declared below are accepted for resynchronization;
// the kernel is not a general-purpose RTP demultiplexer.
package rtp

import (
	"encoding/binary"

	"github.com/pion/rtp"

	"mediad/internal/clock"
	"mediad/internal/model"
)

// Declared payload types for locally generated samples.
const (
	PTVideo = 96 // payload[0] bit0 declares a keyframe
	PTAudio = 97
)

const (
	rtcpSR   = 200
	rtcpRR   = 201
	rtcpNACK = 205 // RTPFB fmt=1
	maxFrame = 9000
)

// Parser consumes chunks of the stream and emits events. Arrival time of a
// packet is the arrival time of the chunk that completed its frame.
type Parser struct {
	buf    []byte
	offset int64 // absolute stream offset of buf[0]
	now    int64 // arrival ms of the newest chunk
}

// Feed appends a chunk that surfaced at arrivalMs on the reference clock
// and returns the events completed by it.
func (p *Parser) Feed(data []byte, arrivalMs int64) []model.Event {
	p.now = arrivalMs
	p.buf = append(p.buf, data...)
	return p.scan(false)
}

// Finish flushes the parser, reporting any truncated tail bytes.
func (p *Parser) Finish() []model.Event {
	return p.scan(true)
}

func (p *Parser) malformed(class model.ErrClass, size int) model.Event {
	return model.Event{
		Kind:       model.KindMalformed,
		Err:        class,
		ByteOffset: p.offset,
		Size:       size,
		ArrivalMs:  p.now,
	}
}

func (p *Parser) scan(final bool) []model.Event {
	var out []model.Event
	for len(p.buf) > 0 {
		if len(p.buf) < 2 {
			if final {
				out = append(out, p.malformed(model.ErrTruncated, len(p.buf)))
				p.drop(len(p.buf))
			}
			break
		}
		length := int(binary.BigEndian.Uint16(p.buf))
		if length < 4 || length > maxFrame {
			if skipped := p.resync(final); skipped > 0 {
				out = append(out, p.malformed(model.ErrBadLength, skipped))
				p.drop(skipped)
				continue
			}
			if final {
				out = append(out, p.malformed(model.ErrBadLength, len(p.buf)))
				p.drop(len(p.buf))
			}
			break
		}
		if len(p.buf) < 2+length {
			// The claimed length is plausible but the bytes are not here.
			// If the header already visible at this boundary is not a
			// declared packet, the prefix is junk: resync instead of
			// waiting for a frame that will never be complete.
			if len(p.buf) >= 4 && !plausible(p.buf[2:]) {
				skipped := p.resync(final)
				if skipped <= 0 {
					skipped = 1
				}
				out = append(out, p.malformed(model.ErrBadLength, skipped))
				p.drop(skipped)
				continue
			}
			if final {
				out = append(out, p.malformed(model.ErrTruncated, len(p.buf)))
				p.drop(len(p.buf))
			}
			break
		}
		frame := p.buf[2 : 2+length]
		// A wrong version at the claimed boundary means the length prefix
		// is junk, not that a real packet is malformed: resync past it.
		if frame[0]>>6 != 2 {
			skipped := p.resync(final)
			if skipped <= 0 {
				skipped = 1
			}
			out = append(out, p.malformed(model.ErrBadLength, skipped))
			p.drop(skipped)
			continue
		}
		if !plausible(frame) {
			// The framing is plausible but the header names no declared
			// packet type: treat the length bytes as junk and advance one
			// byte so a real boundary can be found.
			out = append(out, p.malformed(model.ErrBadLength, 1))
			p.drop(1)
			continue
		}
		ev := p.parseFrame(frame, length)
		out = append(out, ev)
		p.drop(2 + length)
	}
	return out
}

// resync returns how many junk bytes precede the next plausible framed
// boundary, or 0 when no boundary is visible yet (or at all).
func (p *Parser) resync(final bool) int {
	for k := 1; k+2 <= len(p.buf); k++ {
		length := int(binary.BigEndian.Uint16(p.buf[k:]))
		if length < 4 || length > maxFrame {
			continue
		}
		rest := p.buf[k+2:]
		if len(rest) < 2 {
			return k // header not here yet; keep the candidate and wait
		}
		if !plausible(rest) {
			continue
		}
		if len(rest) < length && !final {
			return k // wait for the rest of the candidate frame
		}
		return k
	}
	return 0
}

// plausible reports whether the first bytes of frame identify one of the
// declared RTP payload types or an RTCP packet type. RTCP uses the full
// second byte (200..223), while RTP masks off the marker bit.
func plausible(frame []byte) bool {
	if len(frame) < 2 || frame[0]>>6 != 2 {
		return false
	}
	raw := frame[1]
	if pt := raw & 0x7F; pt == PTVideo || pt == PTAudio {
		return true
	}
	return raw >= rtcpSR && raw <= 223
}

func (p *Parser) drop(n int) {
	p.buf = p.buf[n:]
	p.offset += int64(n)
}

// parseFrame validates one frame. Manual header checks classify errors
// precisely; pion/rtp performs the final unmarshal of valid RTP packets.
func (p *Parser) parseFrame(frame []byte, length int) model.Event {
	base := model.Event{ByteOffset: p.offset + 2, Size: length, ArrivalMs: p.now}
	if frame[0]>>6 != 2 {
		base.Kind, base.Err = model.KindMalformed, model.ErrBadVersion
		return base
	}
	// RTCP packet types occupy 200..223 as the full second byte; declared
	// RTP payload types are 96/97, so there is no ambiguity here.
	if raw := frame[1]; raw >= rtcpSR && raw <= 223 {
		return parseRTCP(frame, base)
	}
	return parseRTP(frame, base)
}

// rtpMeta fills the fixed-header fields that survived validation so that
// malformed packets can still be attributed to a sequence gap. It reports
// whether a declared media type was recovered.
func rtpMeta(frame []byte, ev *model.Event) bool {
	if len(frame) < 12 {
		return false
	}
	ev.Seq16 = binary.BigEndian.Uint16(frame[2:])
	ev.RTPTs = binary.BigEndian.Uint32(frame[4:])
	ev.SSRC = binary.BigEndian.Uint32(frame[8:])
	switch frame[1] & 0x7F {
	case PTVideo:
		ev.Media = model.MediaVideo
		ev.EventMs = clock.RTPClockVideo.ToMillis(uint64(ev.RTPTs))
	case PTAudio:
		ev.Media = model.MediaAudio
		ev.EventMs = clock.RTPClockAudio.ToMillis(uint64(ev.RTPTs))
	default:
		ev.Media = model.MediaUnknown
	}
	return ev.Media != model.MediaUnknown
}

func parseRTP(frame []byte, ev model.Event) model.Event {
	ev.Kind = model.KindMalformed
	rtpMeta(frame, &ev)
	cc := int(frame[0] & 0x0F)
	hdrLen := 12 + 4*cc
	if len(frame) < hdrLen {
		ev.Err = model.ErrTruncated
		return ev
	}
	payloadStart := hdrLen
	if frame[0]&0x10 != 0 { // extension bit
		if len(frame) < hdrLen+4 {
			ev.Err = model.ErrBadExtension
			return ev
		}
		extWords := int(binary.BigEndian.Uint16(frame[hdrLen+2:]))
		payloadStart = hdrLen + 4 + 4*extWords
		if len(frame) < payloadStart {
			ev.Err = model.ErrBadExtension
			return ev
		}
	}
	payloadEnd := len(frame)
	if frame[0]&0x20 != 0 { // padding bit
		pad := int(frame[len(frame)-1])
		if pad == 0 || pad > payloadEnd-payloadStart {
			ev.Err = model.ErrBadPadding
			return ev
		}
		payloadEnd -= pad
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(frame); err != nil {
		ev.Err = model.ErrBadHeader
		return ev
	}
	ev.Kind = model.KindPacket
	ev.Err = model.ErrNone
	ev.SSRC = pkt.SSRC
	ev.Seq16 = pkt.SequenceNumber
	ev.RTPTs = pkt.Timestamp
	switch pkt.PayloadType {
	case PTVideo:
		ev.Media = model.MediaVideo
		ev.EventMs = clock.RTPClockVideo.ToMillis(uint64(pkt.Timestamp))
		if payloadEnd > payloadStart && frame[payloadStart]&0x01 != 0 {
			ev.Keyframe = true
		}
	case PTAudio:
		ev.Media = model.MediaAudio
		ev.EventMs = clock.RTPClockAudio.ToMillis(uint64(pkt.Timestamp))
	default:
		ev.Media = model.MediaUnknown
	}
	if payloadEnd > payloadStart {
		ev.Payload = append([]byte(nil), frame[payloadStart:payloadEnd]...)
	}
	return ev
}

// parseRTCP handles the declared feedback types: SR, RR and NACK.
func parseRTCP(frame []byte, ev model.Event) model.Event {
	ev.Kind = model.KindMalformed
	if len(frame) < 8 {
		ev.Err = model.ErrTruncated
		return ev
	}
	words := int(binary.BigEndian.Uint16(frame[2:]))
	if (words+1)*4 != len(frame) {
		ev.Err = model.ErrBadLength
		return ev
	}
	ev.SSRC = binary.BigEndian.Uint32(frame[4:])
	switch frame[1] {
	case rtcpSR:
		if len(frame) < 28 {
			ev.Err = model.ErrTruncated
			return ev
		}
		ev.Kind = model.KindSenderReport
		ntpSec := binary.BigEndian.Uint32(frame[8:])
		ntpFrac := binary.BigEndian.Uint32(frame[12:])
		// NTP epoch (1900) to Unix epoch offset, in milliseconds.
		ev.NTPMs = (int64(ntpSec)-2208988800)*1000 + int64(ntpFrac)*1000/(1<<32)
		ev.RTPTs = binary.BigEndian.Uint32(frame[16:])
	case rtcpRR:
		ev.Kind = model.KindReceiverReport
	case rtcpNACK:
		if frame[0]&0x1F != 1 { // only fmt=1 (generic NACK) is declared
			ev.Err = model.ErrBadHeader
			return ev
		}
		if len(frame) < 12 {
			ev.Err = model.ErrTruncated
			return ev
		}
		ev.Kind = model.KindNACK
		ev.SSRC = binary.BigEndian.Uint32(frame[8:]) // media source
		for off := 12; off+4 <= len(frame); off += 4 {
			pid := binary.BigEndian.Uint16(frame[off:])
			blp := binary.BigEndian.Uint16(frame[off+2:])
			ev.NACKSeqs = append(ev.NACKSeqs, pid)
			for i := 0; i < 16; i++ {
				if blp&(1<<uint(i)) != 0 {
					ev.NACKSeqs = append(ev.NACKSeqs, pid+uint16(i)+1)
				}
			}
		}
	default:
		ev.Err = model.ErrBadHeader
	}
	return ev
}
