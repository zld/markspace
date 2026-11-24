package sample

import "math"

// Config is the deterministic sample generation configuration. It is embedded
// in checkpoints so a --resume invocation can reconstruct the same source.
type Config struct {
	Seed            int64    `json:"seed"`
	Packets         int      `json:"packets"`
	Loss            float64  `json:"loss"`
	Reorder         float64  `json:"reorder"`
	Duplicate       float64  `json:"duplicate"`
	SSRC            uint32   `json:"ssrc"`
	StartSequence   uint16   `json:"start_sequence"`
	StartTimestamp  uint32   `json:"start_timestamp"`
	SampleRate      uint32   `json:"sample_rate"`
	FrameSamples    uint32   `json:"frame_samples"`
	ContentSize     int      `json:"content_size"`
	KeyFrameEvery   int      `json:"keyframe_every"`
	RTCPEvery       int      `json:"rtcp_every"`
	SuppressRTCP    bool     `json:"suppress_rtcp"`
	CorruptOffset   int64    `json:"corrupt_offset"`
	ProtectedRanges [][2]int `json:"protected_ranges,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		Seed:          17,
		Packets:       12000,
		Loss:          0.02,
		Reorder:       0.08,
		Duplicate:     0.01,
		SSRC:          0x4d445031, // "MDP1"
		StartSequence: 1000,
		// Wrap very early: 24 frames * 960 samples leaves 960 samples before
		// the uint32 timestamp boundary.
		StartTimestamp: math.MaxUint32 - 23*960,
		SampleRate:     48000,
		FrameSamples:   960,
		ContentSize:    142, // yields a fixed 192-byte container RTP chunk
		KeyFrameEvery:  100,
		RTCPEvery:      250,
		ProtectedRanges: [][2]int{
			{400, 599},
			{1000, 1199},
		},
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.Packets <= 0 {
		c.Packets = d.Packets
	}
	if c.SSRC == 0 {
		c.SSRC = d.SSRC
	}
	if c.SampleRate == 0 {
		c.SampleRate = d.SampleRate
	}
	if c.FrameSamples == 0 {
		c.FrameSamples = d.FrameSamples
	}
	if c.ContentSize == 0 {
		c.ContentSize = d.ContentSize
	}
	if c.KeyFrameEvery == 0 {
		c.KeyFrameEvery = d.KeyFrameEvery
	}
	if c.RTCPEvery == 0 {
		c.RTCPEvery = d.RTCPEvery
	}
	if c.StartTimestamp == 0 {
		c.StartTimestamp = d.StartTimestamp
	}
	return c
}

func (c Config) IsProtected(index int) bool {
	for _, r := range c.ProtectedRanges {
		if index >= r[0] && index <= r[1] {
			return true
		}
	}
	return false
}
