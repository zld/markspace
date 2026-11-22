package pipeline

import (
	"bytes"

	pionrtp "github.com/pion/rtp"

	"mediapipeline/internal/container"
	"mediapipeline/internal/media"
	piprtp "mediapipeline/internal/rtp"
	"mediapipeline/internal/timeline"
)

// Type aliases keep the verify cases readable without importing every package
// under a qualified name or aliasing stdlib types.
type (
	timelineEventT = timeline.Event
	intervalT      = timeline.Interval
	rtpConfig      = piprtp.Config
)

func timelineConfig(segment media.Nano) timeline.Config {
	return timeline.Config{
		Segment:             segment,
		AllowedLateness:     100 * media.Nano(int64(media.Nano(1e6))),
		MaxReplayWindow:     20000 * media.Nano(int64(media.Nano(1e6))),
		MaxEventsPerSegment: 10_000,
	}
}

func timelineNew(cfg timeline.Config) (*timeline.Timeline, error) { return timeline.New(cfg) }

func timelineEvent(t media.Nano, track uint32, seq uint64) timeline.Event {
	return timeline.Event{Time: t, Track: track, Seq: seq}
}

func hashSegments(segs []timeline.Segment) string { return timeline.HashSegments(segs) }

func containerParse(data []byte) (*container.Movie, error) {
	return container.Parse(bytes.NewReader(data), int64(len(data)))
}

func rtpNew(cfg piprtp.Config) (*piprtp.Receiver, error) { return piprtp.New(cfg) }

func rtpPacket(seq uint16, ts uint32) *pionrtp.Packet {
	return &pionrtp.Packet{
		Header:  pionrtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, PayloadType: 96},
		Payload: []byte{byte(seq), byte(seq >> 8)},
	}
}
