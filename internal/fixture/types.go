// Package fixture produces deterministic, self-contained H.264/H.265 RTP and
// RTCP test streams. No external media files are referenced: every payload is
// synthesized from a fixed seed so repeated runs yield byte-identical output.
package fixture

import (
	"mediasync/internal/clock"
)

// Scenario selects which network and clock impairments are applied.
type Scenario string

const (
	CaseClean    Scenario = "clean"
	CaseLoss     Scenario = "loss"
	CaseWrap     Scenario = "wrap"
	CaseCorrupt  Scenario = "corrupt"
)

// Config controls one deterministic stream generation run.
type Config struct {
	Seed          int64
	DurationNS    int64
	ClockRate     uint32
	Codec         string // "h264" or "h265"
	SSRCBase      uint32
	TargetBitrates []int // bits per second, one per representation
	GOPFrames     int   // keyframe interval in frames
	KeyframeOffset []int // per-rendition GOP phase offset, enables boundary skew
	BaseFrameNS   int64 // nominal frame period
	VFRAmplitude  int64 // extra jitter in ns added deterministically per frame
	DriftPPM      float64
	ForceWrap     bool  // start the RTP clock near the 32-bit boundary
	LossRate      float64
	ReorderWindow uint16
	ReorderRate   float64
	DuplicateRate float64
	TruncateRate  float64 // fraction of payloads cut short (corrupt case)
}

// DefaultConfig returns the validated config used by the demo and tests.
func DefaultConfig(seed int64, durationNS int64, bitrates []int) (Config, *clock.Error) {
	if len(bitrates) == 0 || len(bitrates) > clock.MaxTracks {
		return Config{}, clock.NewError(clock.ErrInvalidLength, "between 1 and MaxTracks bitrates required")
	}
	if durationNS <= 0 || durationNS > clock.MaxDurationMicros*1000 {
		return Config{}, clock.NewError(clock.ErrInvalidLength, "duration out of bounds")
	}
	// Renditions share GOP alignment by default, which is the normal ABR
	// requirement. Phase skew is injected explicitly to exercise boundary
	// rejection in tests rather than applied to every generated stream.
	offsets := make([]int, len(bitrates))
	return Config{
		Seed:           seed,
		DurationNS:     durationNS,
		ClockRate:      clock.DefaultClockRate,
		Codec:          "h264",
		SSRCBase:       0x4D530000,
		TargetBitrates: append([]int(nil), bitrates...),
		GOPFrames:      15,
		KeyframeOffset: offsets,
		BaseFrameNS:    33_333_333, // ~30 fps
		VFRAmplitude:   2_000_000,
		ReorderWindow:  16,
	}, nil
}

// GroundFrame is the generator's authoritative record of one encoded frame,
// before any network impairment. It is the reference used to score recovery.
type GroundFrame struct {
	Rendition   int
	FrameIndex  int
	Seq         uint16
	IsKeyframe  bool
	RawRTP      uint32 // 32-bit timestamp as it appears on the wire
	ExtendedRTP int64  // unwrapped ground-truth RTP time
	WallNS      int64  // ground-truth capture time in nanoseconds
	PayloadSize int
	PayloadHash byte
}

// Datagram is one serialized RTP packet plus its impairment annotations.
type Datagram struct {
	Rendition   int
	Data        []byte
	Seq         uint16
	SSRC        uint32
	RawRTP      uint32
	Truncated   bool
	Corrupt     bool
	OrigOrder   int // position before network reordering
}

// SenderReportBundle is one serialized RTCP SR with its parsed ground truth.
type SenderReportBundle struct {
	Rendition int
	Data      []byte
	WallNS    int64
	RTP       uint32
	ArrivalNS int64
}

// Stream is the full generated artifact for one run.
type Stream struct {
	Cfg          Config
	Scenario     Scenario
	Renditions   []RenditionStream
	Ground       [][]GroundFrame // indexed by rendition
}

// RenditionStream holds the wire-order packets and RTCP reports of one layer.
type RenditionStream struct {
	SSRC      uint32
	Bitrate   int
	Datagrams []Datagram
	Reports   []SenderReportBundle
}
