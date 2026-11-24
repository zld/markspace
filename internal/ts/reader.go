package ts

import (
	"fmt"

	"medpipe/internal/fault"
)

// ReadResult contains validated packets plus non-fatal and fatal observations.
type ReadResult struct {
	Packets []*Packet
	Events  []fault.Event
}

// ReadPackets validates a complete byte slice as 188-byte TS packets. A short
// tail produces a truncation event and a fault.ErrTruncated error after all
// complete packets have been returned.
func ReadPackets(data []byte) (*ReadResult, *fault.Error) {
	if int64(len(data)) > MaxInputBytes {
		return nil, fault.New(fault.ErrInputTooLarge, "ts.read", 0,
			fmt.Sprintf("input %d exceeds limit %d", len(data), MaxInputBytes))
	}
	res := &ReadResult{}
	cc := NewContinuityMonitor()
	for off := 0; off < len(data); {
		remaining := len(data) - off
		if remaining < PacketSize {
			fe := fault.New(fault.ErrTruncated, "ts.read", int64(off),
				fmt.Sprintf("short TS tail: %d byte(s), want %d", remaining, PacketSize))
			event := fault.Event{
				Code:      fault.ErrTruncated,
				Severity:  "fatal",
				Stage:     "ts.read",
				Offset:    int64(off),
				EndOffset: int64(len(data)),
				Message:   fe.Message,
			}
			// A short tail often still contains the four-byte TS header;
			// attach its PID so the truncation can be placed on a track.
			if remaining >= 4 && data[off] == SyncByte {
				pid := uint16(data[off+1]&0x1f)<<8 | uint16(data[off+2])
				fe = fe.WithPID(pid)
				event.PID = pid
			}
			res.Events = append(res.Events, event)
			return res, fe
		}
		pkt, fe := ParsePacket(data[off:off+PacketSize], int64(off))
		if fe != nil {
			res.Events = append(res.Events, fault.Event{
				Code:     fe.Code,
				Severity: "fatal",
				Stage:    fe.Stage,
				Offset:   fe.Offset,
				PID:      fe.PID,
				Message:  fe.Message,
			})
			return res, fe
		}
		res.Packets = append(res.Packets, pkt)
		res.Events = append(res.Events, cc.Observe(pkt)...)
		off += PacketSize
	}
	return res, nil
}
