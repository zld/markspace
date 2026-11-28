package rtp

import (
	"fmt"

	"github.com/pion/rtp"

	"mediarc/timebase"
)

// place maps a packet onto the timeline. A nil frame means the packet cannot be
// placed at all; a non-empty issue string is a timestamp discontinuity that is
// reported while the packet still takes its own timestamp.
func (w *Window) place(packet *rtp.Packet, seq int64, receivedNS int64) (frame *Frame, errDetail, jumpDetail string) {
	if !w.started {
		w.baseTS = packet.Timestamp
	}
	deltaTicks := int64(int32(packet.Timestamp - w.baseTS))
	start, err := timebase.NanosFromScale(deltaTicks, w.cfg.ClockRate)
	if err != nil {
		return nil, fmt.Sprintf("timestamp %d is not representable on the timeline", packet.Timestamp), ""
	}
	if w.cfg.TimestampJumps && w.havePrev && seq == w.prevSeq+1 {
		want := w.prevTS + uint32(w.cfg.expectedTicks())
		if packet.Timestamp != want {
			jumpDetail = fmt.Sprintf("sequence %d timestamp %d, want %d (+%d ticks)", seq,
				packet.Timestamp, want, w.cfg.expectedTicks())
		}
	}
	payload := make([]byte, len(packet.Payload))
	copy(payload, packet.Payload)
	return &Frame{
		ExtendedSeq:  uint64(seq),
		RTPTimestamp: packet.Timestamp,
		StartNS:      start,
		EndNS:        start + w.cfg.PacketDurationNS,
		DurationNS:   w.cfg.PacketDurationNS,
		Marker:       packet.Marker,
		Payload:      payload,
		ReceivedNS:   receivedNS,
		OutOfOrder:   w.started && seq < w.highest,
	}, "", jumpDetail
}
