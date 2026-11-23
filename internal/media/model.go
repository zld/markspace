// Package media defines the shared data model, the unified media timebase
// and the cross-module error model used by the scan, stream, normalize and
// metrics subsystems.
package media

// AudioMetadata describes the audio track accompanying a frame record.
type AudioMetadata struct {
	Layout     string // e.g. "mono", "stereo", "5.1"
	Channels   int
	SampleRate int // Hz
}

// ColorMetadata carries the color description of a frame as captured.
// Present is false when the capture device supplied no color information.
type ColorMetadata struct {
	Primaries string // e.g. "bt709", "bt2020"
	Transfer  string // e.g. "bt709", "pq", "hlg"
	Matrix    string // e.g. "bt709", "bt2020"
	Range     string // "full" or "limited"
	Present   bool
}

// Frame is one captured image frame record as submitted by the capture side.
// Sequence numbers and timestamps are raw wire values and may wrap.
type Frame struct {
	StreamID     uint32
	Seq          uint16 // wraps modulo 2^16
	Timestamp    uint32 // raw media timestamp in Timescale ticks, wraps modulo 2^32
	Keyframe     bool
	Width        uint16
	Height       uint16
	Size         uint32 // payload bytes actually received
	DeclaredSize uint32 // payload bytes announced by the capture device
	Checksum     uint32 // crc32 of the payload as announced by the source
	PayloadCRC   uint32 // crc32 recomputed over the received payload
	Audio        AudioMetadata
	Color        ColorMetadata
}

// Truncated reports whether fewer payload bytes arrived than declared.
func (f *Frame) Truncated() bool { return f.DeclaredSize > f.Size }

// ChecksumOK reports whether the received payload matches the announced checksum.
func (f *Frame) ChecksumOK() bool { return f.Checksum == f.PayloadCRC }
