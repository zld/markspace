// Package manifest parses DASH MPD and HLS m3u8 playlists into a single
// unified timeline-event model shared by the repair and index subsystems.
package manifest

// Kind identifies the playlist technology an event came from.
type Kind string

const (
	KindDASH Kind = "dash"
	KindHLS  Kind = "hls"
)

// TrackKind distinguishes media tracks inside a representation.
type TrackKind string

const (
	TrackVideo TrackKind = "video"
	TrackAudio TrackKind = "audio"
)

// SegmentRef points at one media segment inside an input file.
type SegmentRef struct {
	Number     int    // one-based media sequence number
	URL        string // synthetic in-workspace relative path
	ByteOffset int64  // offset inside the referenced file, -1 if whole file
	ByteLength int64  // declared length; 0 means unknown
}

// TimelineEvent is the parser output consumed downstream.
//
// StartNS/EndNS are mapped onto the unified integer-nanosecond global
// timeline. Isolated marks a segment that cannot be anchored to the global
// clock (e.g. first segment after an HLS discontinuity with no usable
// instant): the pipeline must keep it isolated rather than guess.
type TimelineEvent struct {
	Kind          Kind
	Representation string
	Track         TrackKind
	Timescale     uint32
	Segment       SegmentRef
	StartNS       int64
	EndNS         int64
	DurationTicks int64
	Discontinuity bool
	DiscoSeq      int
	Isolated      bool
}

// Timeline is the ordered parse result for one whole input set.
type Timeline struct {
	Events []TimelineEvent
}

// append enforces the DASH/HLS rule that segment numbers never repeat
// within one representation.
func (t *Timeline) append(ev TimelineEvent) error {
	for _, e := range t.Events {
		if e.Representation == ev.Representation &&
			e.Segment.Number == ev.Segment.Number {
			return dupNumberError(ev.Representation, ev.Segment.Number)
		}
	}
	t.Events = append(t.Events, ev)
	return nil
}
