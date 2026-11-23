package demux

// FaultCase identifies one of the five self-generated boundary inputs.
type FaultCase string

const (
	FaultNone            FaultCase = ""
	FaultTruncatedIndex  FaultCase = "truncated-index"
	FaultTimestampBack   FaultCase = "timestamp-backward"
	FaultBadLayout       FaultCase = "bad-layout"
	FaultColorConflict   FaultCase = "color-conflict"
	FaultMissingKeyframe FaultCase = "missing-keyframe"
)

// ValidFault reports whether c is a supported injection point.
func ValidFault(c FaultCase) bool {
	switch c {
	case FaultNone, FaultTruncatedIndex, FaultTimestampBack,
		FaultBadLayout, FaultColorConflict, FaultMissingKeyframe:
		return true
	}
	return false
}

// VideoSpec describes one generated video track / representation.
type VideoSpec struct {
	Timescale uint32 // native track timescale, e.g. 24000
	Width     uint16
	Height    uint16
	// KeyframeEvery is the sample interval between sync samples.
	KeyframeEvery uint32
	// FrameDelta is the native stts delta per frame.
	FrameDelta uint32
	// FrameCount is the total number of coded frames.
	FrameCount uint32
	// FrameSizeBytes is the coded size of every frame. When zero, a default is
	// used. Set it from the target bitrate so measured bitrate tracks target.
	FrameSizeBytes uint32
	// Color is the container (colr box) description, zero value means absent.
	Color *ColorValue
	// BitstreamColor adds an mdst bitstream-source description.
	BitstreamColor *ColorValue
}

// FrameSizeForBitrate derives a per-frame coded size so the synthetic stream's
// measured bitrate matches the target. Exact integer math:
//
//	bytes_per_frame = target_bps * frame_delta / (8 * timescale)
func FrameSizeForBitrate(targetBps uint64, timescale, frameDelta uint32) uint32 {
	if timescale == 0 || frameDelta == 0 {
		return 64
	}
	n := targetBps * uint64(frameDelta) / (8 * uint64(timescale))
	if n == 0 {
		n = 1
	}
	return uint32(n)
}

// AudioBitrateBps is the constant synthetic audio bitrate (32 bytes per 1024
// PCM frames at 48 kHz), computed so the video frame budget can subtract it.
const AudioBitrateBps uint64 = 32 * 8 * 48000 / 1024 // 12000 bps

// VideoFrameSizeForTotalBitrate reserves the audio portion first, so the
// combined video+audio measured bitrate tracks the representation target.
func VideoFrameSizeForTotalBitrate(totalTargetBps uint64, timescale, frameDelta uint32) uint32 {
	videoTarget := totalTargetBps
	if totalTargetBps > AudioBitrateBps {
		videoTarget = totalTargetBps - AudioBitrateBps
	}
	return FrameSizeForBitrate(videoTarget, timescale, frameDelta)
}

// ColorValue is the fixture-level color triple.
type ColorValue struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
}

// AudioSpec describes the generated audio track.
type AudioSpec struct {
	Timescale  uint32
	SampleRate uint16
	Channels   uint8 // raw layout code written into mdst (255 = unsupported)
	// FrameSamples is PCM frames per audio sample (stts delta).
	FrameSamples uint32
	FrameCount   uint32
}

// FixtureSpec fully determines one generated container, including injected
// faults. The generator is deterministic: identical specs yield identical bytes.
type FixtureSpec struct {
	Seed     int64
	Duration uint32 // whole seconds, informational
	Video    VideoSpec
	Audio    AudioSpec
	Fault    FaultCase
}
