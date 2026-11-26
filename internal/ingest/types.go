// Package ingest turns RTP packets and fragmented-MP4 samples into a
// unified 48 kHz media timeline. It never invents audio: duplicate
// packets carry no time, missing packets become explicit gaps and
// unlocatable damage stays pending (HOLD).
package ingest

import "audionorm/internal/media"

// Codec identifiers accepted by the pipeline.
type Codec string

const (
	CodecOpus Codec = "opus"
	CodecAAC  Codec = "aac-lc"
)

// Packet is one codec payload placed on the unified timeline.
type Packet struct {
	Codec      Codec
	// ExtSeq is the extended (32-bit) RTP sequence number, or the
	// zero-based sample index for fMP4 input.
	ExtSeq     uint64
	Payload    []byte
	// When is the half-open timeline interval; End == 0 means the
	// duration could not be determined (corrupt / pending).
	When       media.Range
	ByteOffset int64
	// SampleRate is the codec's native clock (e.g. 48000 Opus, 44100 AAC).
	SampleRate uint32
	// Channels is the coded channel count (1 or 2).
	Channels   uint16
}

// Gap is a known, locatable hole on the timeline. No audio is synthesized.
type Gap struct {
	ExtSeq uint64
	When   media.Range
}

// Result is the deterministic output of one ingest pass.
type Result struct {
	Packets    []Packet
	Gaps       []Gap
	Duplicates uint64
	Errors     *media.ErrorLog
}

// NewResult creates an empty ingest result.
func NewResult() *Result {
	return &Result{Errors: media.NewErrorLog()}
}
