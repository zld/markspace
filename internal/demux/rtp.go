package demux

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/rtp"
	"mediapipeline/internal/track"
)

// ParseRTP parses a framed RTP stream into a single-packet-per-sample media
// model.  A sequence break is a hard error carrying the offset of the packet
// whose sequence number is wrong; the verified prefix is not silently
// returned as success.
func ParseRTP(data []byte) (*Media, error) {
	stream, err := rtp.ParseStream(data, rtp.Options{})
	if err != nil {
		switch e := err.(type) {
		case *rtp.ErrIncomplete:
			return nil, fault.Wrap(fault.StageRTP, "rtp", fault.OpTruncated, e.Offset,
				"incomplete RTP packet header", err)
		default:
			return nil, err
		}
	}
	if len(stream.Packets) == 0 {
		return nil, fault.New(fault.StageRTP, "rtp", fault.OpRTPFraming, 0, "no RTP packets in input")
	}
	if len(stream.Damage) > 0 {
		d := stream.Damage[0]
		op := fault.OpSequence
		reason := "RTP sequence number discontinuity"
		if d.Repeated {
			reason = "RTP sequence number repeated"
		}
		return nil, fault.New(fault.StageRTP, "rtp", op, d.Offset, reason)
	}
	tr := Track{
		ID:        stream.SSRC,
		Kind:      track.KindUnknown,
		Timescale: stream.ClockRate,
		Codec:     "rtp",
		Handler:   "",
	}
	for _, p := range stream.Packets {
		tr.Samples = append(tr.Samples, Sample{
			Offset: p.Offset,
			Size:   uint32(p.End - p.Offset),
			DTS:    clock.New(int64(p.Timestamp), stream.ClockRate),
			CTS:    clock.New(int64(p.Timestamp), stream.ClockRate),
			Sync:   true,
			Data:   data[p.Offset:p.End],
		})
	}
	return &Media{Format: "rtp", Tracks: []Track{tr}}, nil
}
