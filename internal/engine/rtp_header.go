package engine

import (
	"mediasync/internal/model"
	"mediasync/internal/rtp"
)

// rtpHeader converts a capture event into the RTP header the sequence layer
// consumes. The raw sequence is masked to 16 bits because the wire field is
// 16 bits; the configured wrap width is applied inside the deduper.
func rtpHeader(ev model.PacketEvent) rtp.Header {
	return rtp.Header{
		TrackID: ev.TrackID, SSRC: ev.SSRC, PayloadTyp: ev.PayloadType,
		Marker: ev.Marker, Seq: uint16(ev.Seq), Timestamp: uint32(ev.Timestamp),
		Arrival: ev.Arrival, Size: ev.Size,
	}
}
