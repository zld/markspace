package media

import "time"

// Frame is one valid, completely decoded media frame. Invalid/truncated RTP
// payloads never become a Frame and therefore cannot enter a segment or
// fingerprint.
type Frame struct {
	SSRC         uint32
	Sequence     uint16
	ExtSequence  uint64
	SourceOffset int64
	PTS          time.Duration
	Duration     time.Duration
	Key          bool
	Energy       int
	ContentType  byte
	Content      []byte
	Hash         [32]byte
}

// End returns the exclusive end time of the frame on the common timeline.
func (f Frame) End() time.Duration { return f.PTS + f.Duration }

// FrameState is the checkpoint-serializable subset of Frame. Content is
// base64-ish through encoding/json []byte encoding.
type FrameState struct {
	SSRC         uint32   `json:"ssrc"`
	Sequence     uint16   `json:"sequence"`
	ExtSequence  uint64   `json:"ext_sequence"`
	SourceOffset int64    `json:"source_offset"`
	PTSNS        int64    `json:"pts_ns"`
	DurationNS   int64    `json:"duration_ns"`
	Key          bool     `json:"key"`
	Energy       int      `json:"energy"`
	ContentType  byte     `json:"content_type"`
	Content      []byte   `json:"content,omitempty"`
	Hash         [32]byte `json:"hash"`
}

func (f Frame) state() FrameState {
	return FrameState{
		SSRC: f.SSRC, Sequence: f.Sequence, ExtSequence: f.ExtSequence,
		SourceOffset: f.SourceOffset, PTSNS: int64(f.PTS), DurationNS: int64(f.Duration),
		Key: f.Key, Energy: f.Energy, ContentType: f.ContentType,
		Content: append([]byte(nil), f.Content...), Hash: f.Hash,
	}
}

func frameFromState(s FrameState) Frame {
	return Frame{
		SSRC: s.SSRC, Sequence: s.Sequence, ExtSequence: s.ExtSequence,
		SourceOffset: s.SourceOffset, PTS: time.Duration(s.PTSNS),
		Duration: time.Duration(s.DurationNS), Key: s.Key, Energy: s.Energy,
		ContentType: s.ContentType, Content: append([]byte(nil), s.Content...), Hash: s.Hash,
	}
}
