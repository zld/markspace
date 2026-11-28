package timeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
)

// Kind identifies a class of timed metadata carried beside the audio/video.
type Kind string

const (
	KindSubtitle Kind = "subtitle"
	KindChapter  Kind = "chapter"
	KindID3      Kind = "id3"
	KindEmsg     Kind = "emsg"
	KindSEI      Kind = "sei"
)

// Source locates the bytes an event was parsed from. A source is carried all
// the way into the manifest so every published cue can be audited against the
// input without re-running the parser.
type Source struct {
	File       string `json:"file"`
	ByteOffset int64  `json:"byte_offset"`
	ByteLength int64  `json:"byte_length"`
	// TrackID is the container track the event belongs to (0 for out-of-band
	// metadata such as a sidecar ID3 tag).
	TrackID uint32 `json:"track_id,omitempty"`
	// Box is the enclosing box type (for example "emsg") or "" for payloads.
	Box string `json:"box,omitempty"`
	// FrameIndex locates an event inside a multi-event source (sample number,
	// RTP sequence position, chapter index). It is 1-based when set.
	FrameIndex int `json:"frame_index,omitempty"`
}

// Event is one timed metadata item placed on the track timeline.
type Event struct {
	Kind Kind `json:"kind"`
	// Time is the event time in its owning track's time base.
	Time Timestamp `json:"-"`
	// Presentation is the exact time in seconds on the shared timeline.
	Presentation Rational `json:"-"`
	// Duration is the event duration when the source declares one.
	Duration Rational `json:"-"`
	// Scheme identifies the payload semantics (ID3 frame id, emsg scheme URI,
	// SEI payload type, subtitle cue id).
	Scheme string `json:"scheme,omitempty"`
	// Payload is the raw metadata bytes.
	Payload []byte `json:"-"`
	Source  Source `json:"source"`
	// Recovered marks events reconstructed from a damaged source. Recovered
	// events are recorded but never published in a manifest.
	Recovered bool `json:"recovered,omitempty"`
}

// TimeMicros returns the presentation time rounded to microseconds.
func (e Event) TimeMicros() (int64, error) { return e.Presentation.Micros() }

// DurationMicros returns the duration rounded to microseconds.
func (e Event) DurationMicros() (int64, error) { return e.Duration.Micros() }

// PayloadSum is the SHA-256 of the payload, used to detect conflicting
// duplicates without retaining payload bytes in the manifest.
func (e Event) PayloadSum() string {
	sum := sha256.Sum256(e.Payload)
	return hex.EncodeToString(sum[:])
}

// ContentKey identifies an event by what it says, independent of which file or
// track delivered it: kind, scheme, instant and payload. Two renditions carrying
// the same cue collapse to one event.
func (e Event) ContentKey() string { return e.identity(true) }

// SlotKey identifies an event by where and when it was found, ignoring its
// payload. A single source slot holding two different payloads is ambiguous.
func (e Event) SlotKey() string { return e.identity(false) }

func (e Event) identity(withPayload bool) string {
	h := sha256.New()
	h.Write([]byte(e.Kind))
	h.Write([]byte{0})
	h.Write([]byte(e.Scheme))
	h.Write([]byte{0})
	us, err := e.Presentation.Micros()
	if err != nil {
		us = 0
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(us))
	h.Write(buf[:])
	if withPayload {
		h.Write([]byte{0})
		h.Write([]byte(e.PayloadSum()))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Key is a stable identity for an event across reruns of the same input,
// including both its source slot and its payload.
func (e Event) Key() string {
	return e.SlotKey() + ":" + e.PayloadSum()[:16]
}

// NewEvent builds an event at the given track timestamp, converting it to the
// shared presentation timeline exactly.
func NewEvent(kind Kind, at Timestamp, duration Rational, scheme string, payload []byte, src Source) (Event, error) {
	secs, err := at.Seconds()
	if err != nil {
		return Event{}, err
	}
	return Event{
		Kind:         kind,
		Time:         at,
		Presentation: secs,
		Duration:     duration,
		Scheme:       scheme,
		Payload:      append([]byte(nil), payload...),
		Source:       src,
	}, nil
}

// EventSet accumulates events from several tracks and normalises them onto one
// timeline. Duplicates are counted once; a duplicate that repeats a different
// payload is reported as a conflict rather than silently resolved.
type EventSet struct {
	Events []Event

	issues     Issues
	duplicates int
	conflicts  int
	byContent  map[string]int // content key -> index into Events
	bySlot     map[string]int // slot key -> index into Events
}

// Add inserts ev. An event repeating both the content and the source slot of a
// retained event is an exact duplicate and is counted once. The same content
// found at a second source location is the same cue delivered twice (a second
// rendition's subtitle copy, say): it is collapsed, and the retained copy is
// always the one at the lowest (file, track, offset) so the choice does not
// depend on the order files happen to be scanned. One source slot holding two
// different payloads is a conflict, because there is no safe way to choose.
func (s *EventSet) Add(ev Event) {
	if s.byContent == nil {
		s.byContent = make(map[string]int)
		s.bySlot = make(map[string]int)
	}
	content, slot := ev.ContentKey(), ev.SlotKey()
	if idx, ok := s.byContent[content]; ok {
		// Same cue re-delivered, possibly from a second rendition. Keep one
		// copy and always retain the lowest source so scan order cannot leak
		// into the manifest digest.
		if sourceLess(ev.Source, s.Events[idx].Source) {
			s.Events[idx] = ev
		}
		s.duplicates++
		return
	}
	if idx, ok := s.bySlot[slot]; ok {
		// Same instant and kind, but different content: ambiguous.
		prev := s.Events[idx]
		s.conflicts++
		s.issues.Add(Issue{
			Severity:   SeverityError,
			Code:       CodeDuplicate,
			File:       ev.Source.File,
			TrackID:    ev.Source.TrackID,
			ByteOffset: ev.Source.ByteOffset,
			Message: fmt.Sprintf("two different %s payloads at one instant (%s offset %d)",
				ev.Kind, prev.Source.File, prev.Source.ByteOffset),
		})
		return
	}
	idx := len(s.Events)
	s.byContent[content] = idx
	s.bySlot[slot] = idx
	s.Events = append(s.Events, ev)
}

// sourceLess orders source positions so a deterministic copy can be retained.
func sourceLess(a, b Source) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.TrackID != b.TrackID {
		return a.TrackID < b.TrackID
	}
	if a.ByteOffset != b.ByteOffset {
		return a.ByteOffset < b.ByteOffset
	}
	return a.FrameIndex < b.FrameIndex
}

// AddError records a parse or time error tied to source location.
func (s *EventSet) AddError(err error) {
	s.issues.AddError(CodeTruncated, err)
}

// Issues returns the accumulated parse problems.
func (s *EventSet) Issues() Issues { return s.issues }

// Duplicates counts exact repeats dropped.
func (s *EventSet) Duplicates() int { return s.duplicates }

// Conflicts counts same-instant events with differing payloads.
func (s *EventSet) Conflicts() int { return s.conflicts }

// Len returns the number of retained events.
func (s *EventSet) Len() int { return len(s.Events) }

// Sort orders events by presentation time, then by kind and source position so
// that equal-time events have one deterministic order across runs and tracks.
func (s *EventSet) Sort() {
	sort.SliceStable(s.Events, func(a, b int) bool {
		x, y := s.Events[a], s.Events[b]
		if c := x.Presentation.Cmp(y.Presentation); c != 0 {
			return c < 0
		}
		if x.Kind != y.Kind {
			return x.Kind < y.Kind
		}
		if x.Scheme != y.Scheme {
			return x.Scheme < y.Scheme
		}
		if x.Source.File != y.Source.File {
			return x.Source.File < y.Source.File
		}
		if x.Source.ByteOffset != y.Source.ByteOffset {
			return x.Source.ByteOffset < y.Source.ByteOffset
		}
		return x.Source.TrackID < y.Source.TrackID
	})
}

// ForManifest returns the events that may be published: recovered events and
// everything after the first unrecoverable parse error are excluded.
func (s *EventSet) ForManifest() []Event {
	out := make([]Event, 0, len(s.Events))
	for _, ev := range s.Events {
		if ev.Recovered {
			continue
		}
		out = append(out, ev)
	}
	return out
}
