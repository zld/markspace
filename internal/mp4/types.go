package mp4

// MuxConfig captures everything the container must record.
type MuxConfig struct {
	Rate          uint32
	Channels      uint16
	Codec         string // four-character sample entry type, e.g. "mp4a"
	BitsPerSample uint16 // PCM sample width in bits (16 or 24)
}

// Candidate is one encoding parameter set the size optimizer may choose.
type Candidate struct {
	BitsPerSample uint16
}

// MuxReport is the structural verification result read back from the file.
type MuxReport struct {
	Bytes          int64   `json:"bytes"`
	TrackDuration  uint64  `json:"trackDuration"` // timescale units
	Timescale      uint32  `json:"timescale"`
	SampleCount    uint32  `json:"sampleCount"`
	SampleRate     uint32  `json:"sampleRate"`
	ChannelCount   uint16  `json:"channelCount"`
	AvgBitratebps  float64 `json:"avgBitrateBps"`
	DurationMicros uint64  `json:"durationMicros"`
}
