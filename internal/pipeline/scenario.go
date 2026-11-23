package pipeline

import (
	"mediaroute/internal/codec"
	"mediaroute/internal/fixture"
	"mediaroute/internal/transport"
)

// DefaultFixtureConfig builds the standard three-track synthetic scenario.
// Track 30 deliberately starts near the uint32 ceiling so its timestamps
// wrap mid-stream, and all tracks use non-zero sequence origins.
func DefaultFixtureConfig(seed int64, lossBurst, window int) fixture.Config {
	return fixture.Config{
		Seed:          seed,
		LossBurst:     lossBurst,
		ReorderWindow: window,
		Duplicates:    4,
		Reorders:      6,
		// The link bandwidth varies continuously; keep the congestion wave
		// strictly inside the configured reorder window so every delayed
		// packet is repairable instead of turning into phantom loss.
		Bandwidth:      true,
		BandwidthDepth: bandwidthDepth(window),
		Tracks: []fixture.TrackSpec{
			// 50 frames * 2 packets * 3 tracks = 300 distinct packets. The
			// single loss burst removes 3 packets on the H.264 track only, so
			// 297/300 survive -> roundtrip_rate = 0.99. Keyframe cadences
			// produce 20 source keyframes; one is truncated by the burst,
			// leaving 19 validated hits -> keyframe_hit_rate = 0.95.
			{SSRC: 10, Codec: codec.H264, Frames: 50, KeyEvery: 10,
				SeqStart: 65000, TSStart: 4_000_000_000, TSIncr: 3003,
				BurstEnabled: true},
			{SSRC: 20, Codec: codec.H265, Frames: 50, KeyEvery: 8,
				SeqStart: 1000, TSStart: 12345, TSIncr: 3003},
			// TSStart is ~22 frames below 2^32 so the increment wraps near
			// the middle of the stream.
			{SSRC: 30, Codec: codec.AV1, Frames: 50, KeyEvery: 8,
				SeqStart: 500, TSStart: 0xFFFF0000, TSIncr: 3003, TSWrap: true},
		},
	}
}

// SegmentCount is the fixed number of committed timeline segments. Complete
// frames are distributed across them in global order; each segment is one
// atomic journal stage.
const SegmentCount = 10

// collectedFrame pairs a reassembled frame with its track metadata. The
// segByFrame map is the owning track input's segment-id slot table.
type collectedFrame struct {
	ssrc       uint32
	c          codec.Codec
	fr         *transport.Frame
	segByFrame map[*transport.Frame]string
}

// bandwidthDepth keeps the congestion wave strictly inside the reorder
// window. A window of 1 disables the wave entirely (depth 0), because any
// delay would be unrepairable and would show up as fake loss.
func bandwidthDepth(window int) int {
	depth := window - 1
	if depth > fixture.CongestionWave {
		depth = fixture.CongestionWave
	}
	if depth < 0 {
		depth = 0
	}
	return depth
}
