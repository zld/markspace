package fixture

// SampleSegment returns the deterministic transport stream for one source
// segment. It is exported for cross-package tests and tooling that need a
// known-good stream without driving the full fixture tree generator.
func SampleSegment(segmentSeq int, renditionOffset int64) []byte {
	return segmentTS(segmentSeq, renditionOffset)
}

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
