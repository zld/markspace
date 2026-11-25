// Package model defines the unified event model shared by the demux,
// mapping, remux, task and merge subsystems, plus the typed error used
// to report failures with stage, track and source byte offset.
package model

import "fmt"

// TrackKind classifies an input or output track.
type TrackKind string

const (
	TrackVideo    TrackKind = "video"
	TrackAudio    TrackKind = "audio"
	TrackSubtitle TrackKind = "subtitle"
	TrackMeta     TrackKind = "metadata"
)

// EventKind classifies a demuxed timed event.
type EventKind string

const (
	EventMedia    EventKind = "media"    // audio/video sample
	EventSubtitle EventKind = "subtitle" // subtitle/caption cue
	EventChapter  EventKind = "chapter"  // chapter mark
	EventID3      EventKind = "id3"      // timed ID3 metadata
	EventEMSG     EventKind = "emsg"     // DASH emsg box
	EventSEI      EventKind = "sei"      // SEI NAL inside a video sample
)

// Track describes one input track.
type Track struct {
	ID        string    `json:"id"` // stable id, e.g. "mp4:1" or "rtp:0"
	Kind      TrackKind `json:"kind"`
	Timescale uint32    `json:"timescale"`
	Handler   string    `json:"handler,omitempty"`
}

// Event is one timed item on the unified tick scale. Payload bytes are
// carried verbatim: the remux path never rewrites media payload.
type Event struct {
	TrackID    string    `json:"track_id"`
	Kind       EventKind `json:"kind"`
	Ticks      int64     `json:"ticks"`   // presentation time, unified scale
	Duration   int64     `json:"duration"`// unified ticks
	SrcOffset  int64     `json:"src_offset"`
	Size       uint32    `json:"size"`
	Seq        int       `json:"seq"` // demux order, stabilises sorting
	Payload    []byte    `json:"payload,omitempty"` // checkpointed as base64
	OutTrackID string    `json:"out_track_id,omitempty"`
	OutOffset  int64     `json:"out_offset,omitempty"`
}

// OutputTrack describes one remux output track and the event kinds it
// accepts. Mapping rules target these IDs.
type OutputTrack struct {
	ID        string     `json:"id"`
	Kind      TrackKind  `json:"kind"`
	Timescale uint32     `json:"timescale"`
	Accepts   []EventKind `json:"accepts"`
}

// AcceptsKind reports whether the output track takes the given event kind.
func (o OutputTrack) AcceptsKind(k EventKind) bool {
	for _, a := range o.Accepts {
		if a == k {
			return true
		}
	}
	return false
}

// Stage names a pipeline phase for checkpoints and error reporting.
type Stage string

const (
	StageDemux    Stage = "demux"
	StageMap      Stage = "map"
	StageRemux    Stage = "remux"
	StageManifest Stage = "manifest"
)

// ErrCategory classifies failures for machine-readable reporting.
type ErrCategory string

const (
	ErrTruncated   ErrCategory = "truncated"
	ErrBadLength   ErrCategory = "bad_length"
	ErrUnsupported ErrCategory = "unsupported"
	ErrWrap        ErrCategory = "wrap"
	ErrDrift       ErrCategory = "drift"
	ErrMapping     ErrCategory = "mapping"
	ErrCheckpoint  ErrCategory = "checkpoint"
	ErrIO          ErrCategory = "io"
)

// Error is the typed failure carried across all subsystems. Offset is the
// raw byte offset in the source when applicable, -1 otherwise.
type Error struct {
	Category ErrCategory `json:"category"`
	Stage    Stage       `json:"stage"`
	TrackID  string      `json:"track_id,omitempty"`
	Offset   int64       `json:"offset"`
	Msg      string      `json:"msg"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s/%s track=%q offset=%d: %s", e.Stage, e.Category, e.TrackID, e.Offset, e.Msg)
}

// NewError builds an *Error; pass offset -1 when not byte-related.
func NewError(cat ErrCategory, stage Stage, trackID string, offset int64, format string, args ...interface{}) *Error {
	return &Error{Category: cat, Stage: stage, TrackID: trackID, Offset: offset, Msg: fmt.Sprintf(format, args...)}
}

// AsError unwraps a generic error into *Error, defaulting to io category.
func AsError(err error, stage Stage) *Error {
	if err == nil {
		return nil
	}
	if me, ok := err.(*Error); ok {
		return me
	}
	return &Error{Category: ErrIO, Stage: stage, Offset: -1, Msg: err.Error()}
}
