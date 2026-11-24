package clock

import "fmt"

// StreamID identifies a media stream in the shared error model.
type StreamID string

const (
	StreamAudio StreamID = "audio"
	StreamVideo StreamID = "video"
)

// ErrorCode is a stable, machine-readable processing outcome.
type ErrorCode string

const (
	CodeOK                        ErrorCode = "OK"
	CodeInvalidPacket             ErrorCode = "INVALID_PACKET"
	CodeDuplicate                 ErrorCode = "DUPLICATE_PACKET"
	CodeOutOfOrder                ErrorCode = "OUT_OF_ORDER"
	CodeMissingPacket             ErrorCode = "MISSING_PACKET"
	CodeTruncatedSample           ErrorCode = "TRUNCATED_SAMPLE"
	CodeAudioRecovered            ErrorCode = "AUDIO_GAP_RECOVERED"
	CodeAudioGapUnrecoverable     ErrorCode = "AUDIO_GAP_UNRECOVERABLE"
	CodeVideoAwaitKeyframe        ErrorCode = "VIDEO_AWAIT_KEYFRAME"
	CodeVideoUnrecoverable        ErrorCode = "VIDEO_UNRECOVERABLE"
	CodeUnknownAdapterVersion     ErrorCode = "UNKNOWN_ADAPTER_VERSION"
	CodeAudioChannelMapped        ErrorCode = "AUDIO_CHANNEL_MAPPED"
	CodeSampleRateConflict        ErrorCode = "AUDIO_SAMPLE_RATE_CONFLICT"
	CodeColorMetadataPreserved    ErrorCode = "VIDEO_COLOR_PRESERVED"
	CodeColorMetadataDefaulted    ErrorCode = "VIDEO_COLOR_DEFAULTED"
	CodeColorMetadataConflict     ErrorCode = "VIDEO_COLOR_CONFLICT"
	CodeCheckpointInvalid         ErrorCode = "CHECKPOINT_INVALID"
	CodeCheckpointNoValidBoundary ErrorCode = "CHECKPOINT_NO_VALID_BOUNDARY"
	CodeRTCPInvalid               ErrorCode = "RTCP_INVALID"
)

// MediaError is comparable and carries enough location information to find a
// damaged packet without reconstructing pipeline-private state.
type MediaError struct {
	Code        ErrorCode `json:"error_code"`
	Stream      StreamID  `json:"stream"`
	SSRC        uint32    `json:"ssrc"`
	Sequence    uint64    `json:"sequence"`
	InputOffset int64     `json:"input_offset"`
	Source      string    `json:"source,omitempty"`
	Message     string    `json:"message,omitempty"`
}

func (e MediaError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s stream=%s seq=%d offset=%d: %s", e.Code, e.Stream, e.Sequence, e.InputOffset, e.Message)
	}
	return fmt.Sprintf("%s stream=%s seq=%d offset=%d", e.Code, e.Stream, e.Sequence, e.InputOffset)
}

// WithSource returns the error with its provenance label set.
func (e MediaError) WithSource(source string) MediaError {
	e.Source = source
	return e
}
