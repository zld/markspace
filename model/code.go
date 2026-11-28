package model

const (
	// CodeInvalidTimecode is returned for unparsable or out-of-range timecodes.
	CodeInvalidTimecode Code = "invalid_timecode"
	// CodeIllegalDropFrame is returned when a drop-frame timecode names a
	// frame that a drop-frame clock never emits.
	CodeIllegalDropFrame Code = "illegal_drop_frame"
	// CodeTimestampOverflow is returned when a conversion would leave int64.
	CodeTimestampOverflow Code = "timestamp_overflow"
	// CodeNegativeTime is returned when a converted timestamp is before zero.
	CodeNegativeTime Code = "negative_time"

	// CodeInvalidCue is returned for a structurally broken SRT/VTT cue.
	CodeInvalidCue Code = "invalid_cue"
	// CodeUnsupportedFormat is returned for input the parser does not know.
	CodeUnsupportedFormat Code = "unsupported_format"

	// CodeInvalidRTP is returned for a packet that cannot be parsed.
	CodeInvalidRTP Code = "invalid_rtp"
	// CodeLatePacket is returned for a packet outside the replay window.
	CodeLatePacket Code = "late_packet"
	// CodeDuplicatePacket is an informational idempotent hit.
	CodeDuplicatePacket Code = "duplicate_packet"
	// CodeTimestampJump is returned when RTP timestamps advance discontinuously
	// instead of by one packet duration.
	CodeTimestampJump Code = "timestamp_jump"
	// CodeSequenceWrap is informational: the 16-bit sequence counter wrapped.
	CodeSequenceWrap Code = "sequence_wrap"

	// CodeTruncatedInput is returned when a read runs past the readable bytes.
	CodeTruncatedInput Code = "truncated_input"
	// CodeCorruptBox is returned for an MP4 box that contradicts its header.
	CodeCorruptBox Code = "corrupt_box"
	// CodeIndexIncomplete is returned when sample tables are missing or lie
	// outside the file.
	CodeIndexIncomplete Code = "index_incomplete"

	// CodeBufferFull is returned when a bounded queue cannot admit a value.
	CodeBufferFull Code = "buffer_full"
	// CodeCanceled is returned when a stage stops because of cancellation.
	CodeCanceled Code = "canceled"
	// CodeClosed is returned when writing to a closed stage.
	CodeClosed Code = "closed"

	// CodeAlignmentDrift is returned when a cue lies farther than the allowed
	// tolerance from its container/RTP anchor.
	CodeAlignmentDrift Code = "alignment_drift"

	// CodeInvalidArgument is returned for bad API or CLI parameters.
	CodeInvalidArgument Code = "invalid_argument"
)
