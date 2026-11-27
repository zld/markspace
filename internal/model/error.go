// Package model defines stable error codes and the shared data structures
// used across parsing, processing, encapsulation and reporting stages.
package model

import "fmt"

// Stage identifies the pipeline stage that produced an error or record.
type Stage string

const (
	StageRTP       Stage = "rtp"
	StageCodec     Stage = "codec"
	StageResample  Stage = "resample"
	StageLayout    Stage = "layout"
	StageLoudness  Stage = "loudness"
	StageMux       Stage = "mux"
	StageCache     Stage = "cache"
	StageCheckpoint Stage = "checkpoint"
)

// Code is a stable, machine-readable error code. Codes never change meaning
// once released so reports and tests can rely on them.
type Code string

const (
	CodeNone              Code = ""
	CodePacketLost        Code = "PACKET_LOST"
	CodePacketDuplicate   Code = "PACKET_DUPLICATE"
	CodeSequenceConflict  Code = "SEQUENCE_CONFLICT"
	CodeTruncatedPacket   Code = "TRUNCATED_PACKET"
	CodeIllegalPacket     Code = "ILLEGAL_PACKET"
	CodeUnsupportedCodec  Code = "UNSUPPORTED_CODEC"
	CodeCacheDigestMismatch Code = "CACHE_DIGEST_MISMATCH"
	CodeCheckpointStale   Code = "CHECKPOINT_STALE"
	CodeBudgetExceeded    Code = "BUDGET_EXCEEDED"
	CodeQualityNotMet     Code = "QUALITY_NOT_MET"
	CodeSizeBudgetExceeded Code = "SIZE_BUDGET_EXCEEDED"
)

// Error carries a stable code together with the pipeline stage, the RTP
// sequence number (when applicable) and the input byte offset where the
// problem was observed. It implements the error interface.
type Error struct {
	Code     Code   `json:"code"`
	Stage    Stage  `json:"stage"`
	Sequence uint16 `json:"sequence"`
	Offset   int64  `json:"offset"`
	Detail   string `json:"detail,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: stage=%s seq=%d offset=%d %s",
		e.Code, e.Stage, e.Sequence, e.Offset, e.Detail)
}

// NewError builds a model.Error.
func NewError(code Code, stage Stage, seq uint16, offset int64, detail string) *Error {
	return &Error{Code: code, Stage: stage, Sequence: seq, Offset: offset, Detail: detail}
}

// Is lets errors.Is match by code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return t.Code == e.Code
}

// Packet is a normalized media packet after RTP ordering: the payload as
// delivered on the wire plus ordering metadata.
type Packet struct {
	Sequence  uint16 // extended RTP sequence number (low 16 bits)
	ExtSeq    uint64 // extended, wrap-free sequence number
	Offset    int64  // byte offset of the packet in the input stream
	Timestamp uint32 // RTP timestamp as received
	Payload   []byte
}

// Material groups one input source's identity and its ordered packets.
type Material struct {
	SourceID string
	Codec    string // "opus" or "aac"
	Rate     uint32 // source sample rate advertised by the stream
	Channels uint8  // source channel count
	Packets  []Packet
}
