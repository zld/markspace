package model

import "time"

// UnitKind classifies a bounded media unit.
type UnitKind string

const (
	KindVideo    UnitKind = "video"
	KindAudio    UnitKind = "audio"
	KindSubtitle UnitKind = "subtitle"
	KindChapter  UnitKind = "chapter"
	KindID3      UnitKind = "id3"
	KindEMSG     UnitKind = "emsg"
	KindSEI      UnitKind = "sei"
)

// Unit is one frame, sample, subtitle, or metadata event on a track.
// All timeline fields are on the common nanosecond timeline.
type Unit struct {
	ID             string    `json:"id"`
	Track          TrackID   `json:"track"`
	Representation string    `json:"representation,omitempty"`
	Kind           UnitKind  `json:"kind"`
	Index          int       `json:"index"`
	Sequence       uint16    `json:"sequence,omitempty"`
	PTSNanos       int64     `json:"pts_nanos"`
	DTSNanos       int64     `json:"dts_nanos"`
	EndNanos       int64     `json:"end_nanos,omitempty"`
	DurationNanos  int64     `json:"duration_nanos"`
	Keyframe       bool      `json:"keyframe,omitempty"`
	SourceOffset   int64     `json:"source_offset"`
	Size           int       `json:"size_bytes"`
	Payload        []byte    `json:"-"`
	EnqueuedAt     time.Time `json:"-"`
}

// Latency returns queue/processing latency measured at dequeue time.
func (u *Unit) Latency(now time.Time) time.Duration {
	if u.EnqueuedAt.IsZero() {
		return 0
	}
	return now.Sub(u.EnqueuedAt)
}

// Event is a normalized timed metadata or subtitle value.
type Event struct {
	Track      TrackID  `json:"track"`
	Kind       UnitKind `json:"kind"`
	Source     string   `json:"source"`
	Offset     int64    `json:"offset"`
	StartNanos int64    `json:"start_nanos"`
	EndNanos   int64    `json:"end_nanos"`
	Text       string   `json:"text,omitempty"`
	Key        string   `json:"key,omitempty"`
	Value      string   `json:"value,omitempty"`
}

// Warning is a non-fatal auditable finding.
type Warning struct {
	Track    TrackID       `json:"track,omitempty"`
	Offset   int64         `json:"offset,omitempty"`
	Category ErrorCategory `json:"category"`
	Message  string        `json:"message"`
}
