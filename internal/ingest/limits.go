package ingest

// Input bounds. Parsing must never allocate or index beyond these.
const (
	// MaxSegments bounds EDL length.
	MaxSegments = 4096
	// MaxPackets bounds one RTP stream.
	MaxPackets = 1 << 20
	// MaxFrameBytes bounds one framed RTP packet on disk.
	MaxFrameBytes = 1 << 16
	// MaxSamples bounds samples per MP4 track.
	MaxSamples = 1 << 22
	// MaxDurationNanos bounds any input time span (1 hour).
	MaxDurationNanos = int64(3600) * 1_000_000_000
	// MaxChunks bounds batch chunk count.
	MaxChunks = 100000
)
