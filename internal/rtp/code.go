package rtp

import (
	"encoding/base64"

	"rtx/internal/model"
)

const (
	// CodeRTPTrace marks malformed traces: bad JSON, bad RTP headers, or
	// declared fields that disagree with the datagram.
	CodeRTPTrace model.Code = "rtp_trace_invalid"
	// CodeRTPSequence marks sequence-number defects that cannot be resolved
	// without guessing (e.g. a jump larger than half the sequence space).
	CodeRTPSequence model.Code = "rtp_sequence_invalid"
)

func decodeBase64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}
