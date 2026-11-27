// Package model defines the media time base and error model shared by every
// mediasync subsystem. All durations and timestamps are integer nanoseconds;
// no subsystem may accumulate time using floating point.
package model

// NS is a point on, or duration along, the unified timeline, in nanoseconds.
type NS int64

// Stage identifies the processing stage that produced a failure. Every stage
// in the pipeline has a stable string so reports remain readable across runs.
type Stage string

const (
	StageInput    Stage = "input"
	StageRTP      Stage = "rtp"
	StageOpus     Stage = "opus"
	StageAAC      Stage = "aac"
	StageMP4      Stage = "mp4"
	StageTimeline Stage = "timeline"
	StageGraph    Stage = "graph"
	StageSpec     Stage = "spec"
	StageConfig   Stage = "config"
	StageManifest Stage = "manifest"
)

// ErrorKind is a stable, machine-readable failure code.
type ErrorKind string

const (
	ErrTooManyPackets    ErrorKind = "too_many_packets"
	ErrPacketTooLarge    ErrorKind = "packet_too_large"
	ErrEmptyPacket       ErrorKind = "empty_packet"
	ErrRTPParse          ErrorKind = "rtp_parse_failed"
	ErrTruncated         ErrorKind = "truncated_packet"
	ErrOpusTOC           ErrorKind = "opus_toc_unsupported"
	ErrOpusFrameCount    ErrorKind = "opus_frame_count_invalid"
	ErrDurationOverflow  ErrorKind = "sample_duration_out_of_bounds"
	ErrAUHeader          ErrorKind = "aac_au_header_illegal"
	ErrAUTruncated       ErrorKind = "aac_au_payload_truncated"
	ErrMP4Box            ErrorKind = "mp4_box_malformed"
	ErrMP4SampleCount    ErrorKind = "mp4_sample_count_mismatch"
	ErrGraphCycle        ErrorKind = "track_dependency_cycle"
	ErrDependencyMissing ErrorKind = "track_dependency_unsatisfied"
	ErrBudgetExceeded    ErrorKind = "processing_budget_exceeded"
	ErrBitrateExceeded   ErrorKind = "target_bitrate_unsatisfiable"
	ErrQualityBelowFloor ErrorKind = "quality_below_threshold"
)

// Failure is the single error model used by all stages. PacketOffset is the
// zero-based offset of the packet inside its source track; track-level
// failures use NoPacket. Failures never get silently dropped.
type Failure struct {
	TrackID      string    `json:"track_id"`
	Stage        Stage     `json:"stage"`
	PacketOffset int       `json:"packet_offset"`
	Code         ErrorKind `json:"code"`
	Reason       string    `json:"reason"`
}

// NoPacket marks a failure that is not scoped to a single packet.
const NoPacket = -1

// Error implements the error interface.
func (f Failure) Error() string {
	if f.PacketOffset == NoPacket {
		return string(f.Stage) + "/" + string(f.Code) + ": " + f.Reason
	}
	return string(f.Stage) + "/" + string(f.Code) + " packet[" +
		itoa(f.PacketOffset) + "]: " + f.Reason
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// NewFailure builds a packet-scoped failure.
func NewFailure(track string, stage Stage, offset int, code ErrorKind, reason string) Failure {
	return Failure{TrackID: track, Stage: stage, PacketOffset: offset, Code: code, Reason: reason}
}

// NewTrackFailure builds a track-level failure.
func NewTrackFailure(track string, stage Stage, code ErrorKind, reason string) Failure {
	return Failure{TrackID: track, Stage: stage, PacketOffset: NoPacket, Code: code, Reason: reason}
}

// Parsing bounds. Inputs exceeding these limits are rejected rather than
// parsed, so malformed local assets cannot exhaust the process.
const (
	MaxPacketsPerTrack = 1 << 18 // 262,144 RTP packets per track
	MaxPacketBytes     = 1 << 16 // 64 KiB per RTP packet
	MaxTracks          = 64
	// MaxOpusPacketNS is the largest legal self-delimiting Opus packet
	// (three 40 ms frames plus the 2.5 ms decode bonus = 122.5 ms); we
	// round down to the largest whole-frame duration we accept.
	MaxOpusPacketNS NS = 120 * 1_000_000
)

// CodecKind enumerates the payload codecs the kernel understands.
type CodecKind string

const (
	CodecOpus  CodecKind = "opus"
	CodecAAC   CodecKind = "aac"
	CodecVideo CodecKind = "video"
)

// ColorMeta carries the colour description metadata checked against target
// delivery specs (ITU-T H.274 / ISO 23001-8 code points).
type ColorMeta struct {
	ColorPrimaries         uint16 `json:"color_primaries"`
	TransferCharacteristics uint16 `json:"transfer_characteristics"`
	MatrixCoefficients     uint16 `json:"matrix_coefficients"`
	FullRange              bool   `json:"full_range"`
}

// TrackMeta describes one source track's static layout.
type TrackMeta struct {
	TrackID    string     `json:"track_id"`
	Codec      CodecKind  `json:"codec"`
	ClockRate  uint32     `json:"clock_rate_hz"`
	Channels   uint16     `json:"channels"`
	SampleRate uint32     `json:"sample_rate_hz"`
	Color      *ColorMeta `json:"color,omitempty"`
}

// RawPacket is one RTP datagram plus its stable source offset.
type RawPacket struct {
	Offset int    `json:"offset"`
	RTP    []byte `json:"-"`
}

// TrackInput is everything known about one source track before processing.
// MP4Data is only set for AAC tracks validated against an MP4 sample table.
type TrackInput struct {
	Meta      TrackMeta   `json:"meta"`
	Packets   []RawPacket `json:"-"`
	MP4Data   []byte      `json:"-"`
	DependsOn []string    `json:"depends_on,omitempty"`
}

// TargetSpec is the delivery specification every track is checked against.
// Zero values mean "no constraint for this field".
type TargetSpec struct {
	Channels   uint16     `json:"channels,omitempty"`
	SampleRate uint32     `json:"sample_rate_hz,omitempty"`
	Color      *ColorMeta `json:"color,omitempty"`
	MinQuality int        `json:"min_quality,omitempty"` // 0..100
	MaxBitrate int64      `json:"max_bitrate_bps,omitempty"`
	BudgetNS   NS         `json:"budget_ns,omitempty"`
}
