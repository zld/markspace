package adapter

import (
	"encoding/binary"
	"io"

	"github.com/pion/rtp"

	"continuity/internal/core"
)

// Event is one RTP event record with its reference-clock arrival time.
type Event struct {
	ArrivalRef int64  // reference-clock ticks at arrival
	RTPTime    uint32 // raw 32-bit RTP timestamp (source clock)
	Seq        uint16
	SSRC       uint32
}

// RTPParser parses event-record files. Record framing:
// [8B arrival ref ticks BE][2B packet length BE][RTP packet].
type RTPParser struct{}

// Version reports the parser contract version.
func (RTPParser) Version() int { return 1 }

// ParseEvents decodes all records; a malformed record is reported with
// its byte offset in the source.
func (RTPParser) ParseEvents(r io.Reader, source string) ([]Event, error) {
	var out []Event
	off := int64(0)
	for {
		var hdr [10]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				break
			}
			return nil, core.NewError(core.StageParse, core.CodeTruncatedBox, source, 0, off,
				"event record header truncated", err)
		}
		arr := int64(binary.BigEndian.Uint64(hdr[0:8]))
		n := int(binary.BigEndian.Uint16(hdr[8:10]))
		if n < 12 {
			return nil, core.NewError(core.StageParse, core.CodeInput, source, 0, off,
				"rtp packet too short", nil)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, core.NewError(core.StageParse, core.CodeTruncatedBox, source, 0, off,
				"rtp packet truncated", err)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf); err != nil {
			return nil, core.NewError(core.StageParse, core.CodeInput, source, 0, off,
				"rtp unmarshal", err)
		}
		out = append(out, Event{
			ArrivalRef: arr,
			RTPTime:    pkt.Timestamp,
			Seq:        pkt.SequenceNumber,
			SSRC:       pkt.SSRC,
		})
		off += 10 + int64(n)
	}
	return out, nil
}
