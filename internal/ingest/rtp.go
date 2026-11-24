package ingest

import (
	"encoding/binary"
	"time"

	"github.com/pion/rtp"

	"medpipe/internal/clock"
	"medpipe/internal/media"
)

const (
	// DefaultMaxPacketBytes bounds one input packet and therefore the maximum
	// per-packet memory in addition to the queue byte budget.
	DefaultMaxPacketBytes = 64 * 1024
)

// Item is the parser-kernel boundary object. Parser adapters put all failures
// in Err as clock.Error; they never panic or leak adapter-specific types.
type Item struct {
	Kind      string
	SSRC      uint32
	Sequence  uint16
	ExtSeq    uint64
	Timestamp uint32
	ExtTS     uint64
	Offset    int64
	WireBytes int
	Point     clock.Point
	Frame     *media.Frame
	Reports   []clock.SenderReport
	Err       *clock.Error
}

// RTPParser parses pion RTP packets and medpipe frame payloads.
type RTPParser struct {
	MaxPacketBytes int
	SampleRate     uint32
}

func NewRTPParser(sampleRate uint32) *RTPParser {
	return &RTPParser{MaxPacketBytes: DefaultMaxPacketBytes, SampleRate: sampleRate}
}

func (p *RTPParser) Parse(raw []byte, sourceOffset int64, timeline *clock.Timeline, arrival time.Time) Item {
	max := p.MaxPacketBytes
	if max <= 0 {
		max = DefaultMaxPacketBytes
	}
	loc := headerLocation(raw, sourceOffset)
	item := Item{
		Kind: "rtp", Offset: sourceOffset, WireBytes: len(raw),
		SSRC: loc.SSRC, Sequence: loc.Sequence, Timestamp: loc.Timestamp,
	}

	if len(raw) == 0 {
		item.Err = clock.Errorf(clock.CodeInvalidPacket, loc, "empty RTP packet")
		return item
	}
	if len(raw) > max {
		item.Err = clock.Errorf(clock.CodePacketTooLarge, loc,
			"RTP packet length %d exceeds maximum %d", len(raw), max)
		return item
	}
	if raw[0]>>6 != 2 {
		item.Err = clock.Errorf(clock.CodeInvalidPacket, loc, "unsupported RTP version %d", raw[0]>>6)
		return item
	}

	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		if len(raw) >= 12 {
			item.ExtSeq = timeline.ExtendSequence(loc.SSRC, loc.Sequence)
			item.ExtTS = timeline.ExtendTimestamp(loc.SSRC, loc.Timestamp)
		}
		item.Err = clock.Wrap(clock.CodeInvalidPacket, loc, err, "RTP unmarshal failed")
		return item
	}
	if pkt.Header.SSRC == 0 {
		item.Err = clock.Errorf(clock.CodeMissingField, loc, "RTP packet missing nonzero SSRC")
		return item
	}

	point := timeline.Observe(pkt.Header.SSRC, pkt.Header.SequenceNumber, pkt.Header.Timestamp, arrival)
	item.SSRC = pkt.Header.SSRC
	item.Sequence = pkt.Header.SequenceNumber
	item.Timestamp = pkt.Header.Timestamp
	item.ExtSeq = point.ExtSequence
	item.ExtTS = point.ExtTS
	item.Point = point

	frame, perr := DecodePayload(pkt.Payload, loc, p.SampleRate, point)
	if perr != nil {
		item.Err = perr
		return item
	}
	item.Frame = frame
	return item
}

func headerLocation(raw []byte, offset int64) *clock.Location {
	loc := &clock.Location{ByteOffset: offset, PacketByteOffset: offset}
	if len(raw) >= 12 {
		loc.Sequence = binary.BigEndian.Uint16(raw[2:4])
		loc.Timestamp = binary.BigEndian.Uint32(raw[4:8])
		loc.SSRC = binary.BigEndian.Uint32(raw[8:12])
	}
	return loc
}

// ParseFrameOnly is used by tests that already have a timeline point.
func ParseFrameOnly(raw []byte, loc *clock.Location, rate uint32, point clock.Point) (*media.Frame, *clock.Error) {
	return DecodePayload(raw, loc, rate, point)
}
