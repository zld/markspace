// Package media defines the shared frame model, rational time base and
// classified errors used by every pipeline stage.
package media

import "crypto/sha256"

// MetaVersion is the only metadata version this kernel accepts.
const MetaVersion uint8 = 1

// FrameState records how a frame entered the committed timeline.
type FrameState uint8

const (
	StateClean       FrameState = iota // arrived in order, verified
	StateRecovered                     // arrived out of order but before commit
	StatePlaceholder                   // synthesized gap filler, no payload
	StateDegraded                      // verified but after a transport gap
)

func (s FrameState) String() string {
	switch s {
	case StateClean:
		return "clean"
	case StateRecovered:
		return "recovered"
	case StatePlaceholder:
		return "placeholder"
	case StateDegraded:
		return "degraded"
	}
	return "unknown"
}

// Meta is the project-defined RTP metadata subset carried per frame.
type Meta struct {
	Version uint8
	Flags   uint8
	Length  uint32 // expected payload length in bytes
}

// Frame is the unit exchanged between all stages.
type Frame struct {
	StreamID uint32
	Seq      uint64 // frame sequence number within the stream
	Ticks    int64  // capture time in Clock ticks
	Key      bool
	Meta     Meta
	Sum      [sha256.Size]byte
	Payload  []byte
	State    FrameState
}

// ComputeSum returns the SHA-256 of the payload.
func (f *Frame) ComputeSum() [sha256.Size]byte { return sha256.Sum256(f.Payload) }

// Clone returns a deep copy safe to mutate independently.
func (f *Frame) Clone() *Frame {
	c := *f
	c.Payload = append([]byte(nil), f.Payload...)
	return &c
}
