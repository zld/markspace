package fixture

// SampleSegment returns the deterministic transport stream for one source
// segment. It is exported for cross-package tests and tooling that need a
// known-good stream without driving the full fixture tree generator.
func SampleSegment(segmentSeq int, renditionOffset int64) []byte {
	return segmentTS(segmentSeq, renditionOffset)
}

// NewMuxerForTest exposes the deterministic muxer for cross-package tests.
func NewMuxerForTest() *Muxer { return NewMuxer() }

// IDRFrameForTest returns one synthetic IDR access unit.
func IDRFrameForTest(index int) []byte { return videoFrame(true, index) }

// WriteVideoForTest emits one timestamped video PES packet.
func (m *Muxer) WriteVideoForTest(au []byte, pts int64) {
	m.writePayload(videoPID, true, buildPES(0xE0, pts, au))
}

// BytesForTest returns the muxed bytes.
func (m *Muxer) BytesForTest() []byte { return m.Bytes() }

// ContinuousStream builds one muxer stream containing idrCount IDR access
// units with continuous PTS and continuity counters, unlike concatenating
// independent SampleSegment outputs (each of which restarts its CC state).
func ContinuousStream(idrCount int) []byte {
	m := NewMuxer()
	total := idrCount * framesPerSegment
	for f := 0; f < total; f++ {
		pts := int64(f) * ticksPerFrame
		idr := f%framesPerSegment == 0
		video := videoFrame(idr, f)
		m.writePayload(videoPID, true, buildPES(0xE0, pts, video))
		m.writePayload(audioPID, true, buildPES(0xC0, pts, audioFrame(f)))
		m.maybePSI()
	}
	return m.Bytes()
}
