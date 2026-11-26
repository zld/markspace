package fixture

import (
	"encoding/binary"
	"io"

	"github.com/pion/rtp"

	"mediamerge/internal/fault"
	"mediamerge/internal/rtpclock"
)

// rtpRecordSize is the fixed sideband record width (seq u16, reserved u16, ts u32).
const rtpRecordSize = 8

// ReadRTPStream parses the generated RTP sideband through the rtpclock
// unwrapper and returns the per-packet events in receive order.
func ReadRTPStream(source string, r io.Reader, clockRate uint32) ([]rtpclock.Event, error) {
	stream, err := rtpclock.NewStream(source, clockRate)
	if err != nil {
		return nil, err
	}
	var events []rtpclock.Event
	buf := make([]byte, rtpRecordSize)
	for {
		n, err := io.ReadFull(r, buf)
		if err == io.EOF {
			break
		}
		if err == io.ErrUnexpectedEOF {
			return nil, fault.New(fault.StageRTP, fault.KindTruncated, source,
				"truncated RTP sideband record")
		}
		if err != nil {
			return nil, fault.New(fault.StageIO, fault.KindIO, source, err.Error())
		}
		if n != rtpRecordSize {
			return nil, fault.New(fault.StageRTP, fault.KindTruncated, source, "short RTP record")
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				SequenceNumber: binary.BigEndian.Uint16(buf[0:2]),
				Timestamp:      binary.BigEndian.Uint32(buf[4:8]),
			},
		}
		ev, err := stream.Observe(pkt)
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, nil
}
