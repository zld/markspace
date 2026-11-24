package ingest

import (
	"encoding/binary"
	"io"
	"strconv"

	"github.com/pion/rtp"

	"mediad/internal/clock"
	"mediad/internal/mederr"
)

// RTPPacket is one parsed RTP packet with its raw and unwrapped
// timestamp plus the local arrival time from the capture clock.
type RTPPacket struct {
	Seq       uint16
	RawTS     uint32 // raw 32-bit RTP timestamp, always preserved
	Extended  int64  // monotonically unwrapped timestamp in clock-rate ticks
	ArrivalNs int64  // local capture clock, nanoseconds
	Payload   []byte
}

// RTPStream is a parsed packet stream on a named source clock.
type RTPStream struct {
	ClockRate uint32 // RTP clock ticks per second
	Packets   []RTPPacket
}

// ReadRTPStream reads length-prefixed RTP packets:
//
//	[uint32 BE payloadLen][int64 BE arrivalNs][rtp packet bytes]
//
// Bad packets and non-monotonic timestamps are recorded as recoverable,
// locatable errors and skipped; parsing continues with the next frame.
// The 32-bit timestamp is unwrapped monotonically across wrap events.
func ReadRTPStream(r io.Reader, clockRate uint32, track string, errs *mederr.List) (*RTPStream, error) {
	if clockRate == 0 {
		return nil, mederr.New(mederr.CatParse, track, -1, -1, false, "zero RTP clock rate")
	}
	st := &RTPStream{ClockRate: clockRate}
	var uw clock.Unwrapper
	hdr := make([]byte, 12)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF {
				break
			}
			if err == io.ErrUnexpectedEOF {
				errs.Add(mederr.New(mederr.CatParse, track, -1, -1, true,
					"truncated frame header at packet %d", len(st.Packets)))
				break
			}
			return nil, mederr.New(mederr.CatIO, track, -1, -1, false, "read: %v", err)
		}
		n := binary.BigEndian.Uint32(hdr[0:4])
		arrival := int64(binary.BigEndian.Uint64(hdr[4:12]))
		if n > MaxFrameBytes {
			return nil, mederr.New(mederr.CatLimit, track, -1, -1, false,
				"frame of %d bytes exceeds limit %d", n, MaxFrameBytes)
		}
		if len(st.Packets) >= MaxPackets {
			return nil, mederr.New(mederr.CatLimit, track, -1, -1, false,
				"packet count exceeds limit %d", MaxPackets)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			errs.Add(mederr.New(mederr.CatParse, track, -1, -1, true,
				"truncated packet body: %v", err))
			break
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf); err != nil {
			errs.Add(mederr.New(mederr.CatParse, track, -1, -1, true,
				"bad RTP packet at frame %d: %v", len(st.Packets), err))
			continue
		}
		ext, err := uw.Unwrap(pkt.Timestamp)
		if err != nil {
			if me, ok := err.(*mederr.Error); ok {
				me.Track = track
				me.Message = "seq " + strconv.Itoa(int(pkt.SequenceNumber)) + ": " + me.Message
				errs.Add(me)
			}
			continue
		}
		st.Packets = append(st.Packets, RTPPacket{
			Seq:       pkt.SequenceNumber,
			RawTS:     pkt.Timestamp,
			Extended:  ext,
			ArrivalNs: arrival,
			Payload:   pkt.Payload,
		})
	}
	return st, nil
}
