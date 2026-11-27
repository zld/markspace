package model

import "regress/internal/clock"

// TrackKind classifies a media or metadata track.
type TrackKind string

const (
	KindVideo    TrackKind = "video"
	KindAudio    TrackKind = "audio"
	KindSubtitle TrackKind = "subtitle"
	KindChapter  TrackKind = "chapter"
	KindID3      TrackKind = "id3"
	KindEmsg     TrackKind = "emsg"
	KindSEI      TrackKind = "sei"
)

// Event is a timed cross-track metadata item (emsg / ID3 / SEI / chapter /
// subtitle). ExpectedTick is the planned media time from the generation
// plan; ObservedTick is the time reconstructed from the transport stream.
// Delay is ObservedTick minus ExpectedTick on the master time line.
type Event struct {
	Key          string     `json:"key"`           // stable task key used for dedup
	Track        string     `json:"track"`         // source track identifier
	Kind         TrackKind  `json:"kind"`          // event payload kind
	Seq          uint32     `json:"seq"`           // original source sequence number
	ExpectedTick clock.Tick `json:"expected_tick"` // planned presentation time
	ObservedTick clock.Tick `json:"observed_tick"` // reconstructed presentation time
	Delay        clock.Tick `json:"delay"`         // observed - expected
	PayloadHash  string     `json:"payload_hash"`  // sha256 of payload, hex
}

// Sample is one decoded media sample with timing in master ticks and enough
// frame data for offline evidence statistics.
type Sample struct {
	Seq         uint32     `json:"seq"`
	Tick        clock.Tick `json:"tick"` // presentation time in master ticks
	Width       int        `json:"width,omitempty"`
	Height      int        `json:"height,omitempty"`
	Frame       []byte     `json:"-"` // grayscale pixels for video
	Audio       []int16    `json:"-"` // PCM samples for audio
	PayloadHash string     `json:"payload_hash"`
}

// Track is one parsed rendition or metadata track.
type Track struct {
	ID        string    `json:"id"`
	Kind      TrackKind `json:"kind"`
	Timescale int64     `json:"timescale"` // source Hz
	Samples   []Sample  `json:"samples,omitempty"`
	Events    []Event   `json:"events,omitempty"`
}

// PlanItem is one ground-truth entry of the deterministic generation plan:
// it states what the harness intended to publish at which media time.
type PlanItem struct {
	Key          string     `json:"key"`
	Track        string     `json:"track"`
	Kind         TrackKind  `json:"kind"`
	Seq          uint32     `json:"seq"`
	ExpectedTick clock.Tick `json:"expected_tick"`
	// DelayTicks, when non-zero, makes the fixture writer emit the item
	// shifted by this many master ticks (a reproducible publish delay).
	DelayTicks clock.Tick `json:"delay_ticks,omitempty"`
	// Drop makes the writer omit the item from the transport stream while
	// keeping it in the plan, modelling a lost event.
	Drop bool `json:"drop,omitempty"`
	// Duplicate makes the writer emit the item a second time with the same
	// stable key, modelling a republish.
	Duplicate bool `json:"duplicate,omitempty"`
}
