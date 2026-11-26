package scan

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"

	"mediad/internal/model"
)

// RTP clocks for the SEI tracks we accept. 90 kHz is mandated for H.264/H.265
// video by RFC 6184 / RFC 7798.
const rtpClockRate int64 = 90000

// maxNALUSize bounds a reassembled NAL unit. SEI NALUs are small metadata; a
// reassembly beyond this is abandoned and reported rather than buffered
// without limit.
const maxNALUSize = 8 << 20 // 8 MiB

// RTPSEIExtractor consumes RTP packets carrying H.264 or H.265, reassembles
// NAL units across FU fragmentation and emits SEI timed events. Packet order
// anomalies are surfaced, never silently reordered.
type RTPSEIExtractor struct {
	codec     string // "h264" or "h265"
	trackID   uint32
	base      model.Rational
	lastSeq   uint16
	hasSeq    bool
	lastTS    uint32
	hasTS     bool
	frag      *fragment
	streamOff int64 // running byte offset across the RTP stream

	Events    []TimedEvent
	Anomalies []model.Anomaly
}

type fragment struct {
	nalType byte
	buf     []byte
	ts      uint32
	off     int64
}

// NewRTPSEIExtractor builds an extractor for the given codec and track.
func NewRTPSEIExtractor(codec string, trackID uint32) *RTPSEIExtractor {
	return &RTPSEIExtractor{
		codec:   codec,
		trackID: trackID,
		base:    model.Rational{Num: 1, Den: rtpClockRate},
	}
}

// Push feeds one raw RTP packet. packetOffset is its byte offset in the
// source stream so events and anomalies keep a source location.
func (x *RTPSEIExtractor) Push(raw []byte, packetOffset int64) error {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		x.Anomalies = append(x.Anomalies, model.Anomaly{
			Type: model.AnomalyUnparseable, TrackID: x.trackID,
			Message:      "unparseable RTP packet: " + err.Error(),
			SourceOffset: packetOffset, TimeBase: x.base,
		})
		return nil
	}
	x.streamOff = packetOffset
	x.checkOrder(pkt, packetOffset)

	switch x.codec {
	case "h264":
		return x.handleH264(&pkt, packetOffset)
	case "h265":
		return x.handleH265(&pkt, packetOffset)
	default:
		return fmt.Errorf("scan: unsupported RTP SEI codec %q", x.codec)
	}
}

// checkOrder records out-of-order packets and RTP timestamp wrap/backsteps.
func (x *RTPSEIExtractor) checkOrder(pkt rtp.Packet, off int64) {
	if x.hasSeq {
		want := x.lastSeq + 1
		if pkt.SequenceNumber != want {
			kind := model.AnomalyOutOfOrder
			if pkt.SequenceNumber == x.lastSeq {
				kind = model.AnomalyDuplicate
			}
			x.Anomalies = append(x.Anomalies, model.Anomaly{
				Type: kind, TrackID: x.trackID,
				Message:      fmt.Sprintf("seq got %d want %d", pkt.SequenceNumber, want),
				SourceOffset: off, TimeBase: x.base, RawTick: int64(pkt.Timestamp),
			})
			// A discontinuity invalidates any in-progress fragment.
			x.frag = nil
		}
	}
	x.lastSeq = pkt.SequenceNumber
	x.hasSeq = true

	if x.hasTS && int32(pkt.Timestamp-x.lastTS) < 0 {
		x.Anomalies = append(x.Anomalies, model.Anomaly{
			Type: model.AnomalyTimeWrap, TrackID: x.trackID,
			Message:      "RTP timestamp moved backwards",
			SourceOffset: off, TimeBase: x.base, RawTick: int64(pkt.Timestamp),
		})
	}
	x.lastTS = pkt.Timestamp
	x.hasTS = true
}

func (x *RTPSEIExtractor) emitSEI(nal []byte, ts uint32, off int64) {
	x.Events = append(x.Events, TimedEvent{
		TrackID: x.trackID, Kind: EventSEI, Tick: int64(ts),
		Offset: off, Length: int64(len(nal)), TimeBase: x.base,
		ID:      fmt.Sprintf("sei-%d-%d", x.trackID, len(x.Events)),
		Payload: append([]byte(nil), nal...),
	})
}

func (x *RTPSEIExtractor) abandon(off int64, why string) {
	x.Anomalies = append(x.Anomalies, model.Anomaly{
		Type: model.AnomalyGap, TrackID: x.trackID, Message: why,
		SourceOffset: off, TimeBase: x.base,
	})
	x.frag = nil
}

// isSEI reports whether a reassembled NAL type is an SEI for the codec.
func (x *RTPSEIExtractor) isSEI(nalType byte) bool {
	if x.codec == "h264" {
		return nalType == 6
	}
	return nalType == 39 || nalType == 40 // PREFIX_SEI / SUFFIX_SEI
}

// parseSEIPayload returns the SEI message bytes following the NAL header and
// any emulation-prevention bytes removed, used only to confirm parseability.
func parseSEIMessages(nal []byte, headerLen int) ([][]byte, bool) {
	rbsp := removeEPB(nal[headerLen:])
	if len(rbsp) == 0 {
		return nil, false
	}
	var msgs [][]byte
	i := 0
	for i < len(rbsp) {
		// RBSP stop bit: 0x80 followed by zero or more alignment 0x00 bytes
		// marks the end of the SEI payload, not another message.
		if rbsp[i] == 0x80 {
			for i < len(rbsp) && rbsp[i] == 0x00 {
				i++
			}
			break
		}
		if rbsp[i] == 0x00 {
			// Tolerate alignment padding between messages.
			i++
			continue
		}
		payloadType := 0
		for i < len(rbsp) && rbsp[i] == 0xFF {
			payloadType += 0xFF
			i++
		}
		if i >= len(rbsp) {
			return nil, false
		}
		payloadType += int(rbsp[i])
		i++
		size := 0
		for i < len(rbsp) && rbsp[i] == 0xFF {
			size += 0xFF
			i++
		}
		if i >= len(rbsp) {
			return nil, false
		}
		size += int(rbsp[i])
		i++
		if i+size > len(rbsp) {
			return nil, false
		}
		msg := append([]byte{byte(payloadType)}, rbsp[i:i+size]...)
		msgs = append(msgs, msg)
		i += size
	}
	return msgs, true
}

// removeEPB strips H.264/H.265 emulation prevention bytes (0x03 after 00 00).
func removeEPB(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if i+2 < len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 3 {
			out = append(out, 0, 0)
			i += 2
			continue
		}
		out = append(out, b[i])
	}
	return out
}

// u16be is a small helper for H.265 header reconstruction.
func u16be(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
