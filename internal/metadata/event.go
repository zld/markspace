// Package metadata implements cancellation-neutral, cross-track timed events
// (subtitles, chapters, ID3, emsg and SEI) on an exact unified time base.
package metadata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"mediapipeline/internal/clock"
)

// Kind identifies the source of a timed event.
type Kind string

const (
	KindSubtitle Kind = "subtitle"
	KindChapter  Kind = "chapter"
	KindID3      Kind = "id3"
	KindEmsg     Kind = "emsg"
	KindSEI      Kind = "sei"
)

// Valid reports whether k is a supported event kind.
func (k Kind) Valid() bool {
	switch k {
	case KindSubtitle, KindChapter, KindID3, KindEmsg, KindSEI:
		return true
	}
	return false
}

// Event is one timed metadata item on a specific track.
//
// Seq preserves the demux order and is only used as a final tie-breaker, so
// sorting the same input twice always yields the same order.
type Event struct {
	Kind    Kind
	TrackID uint32
	Time    clock.Time
	// Scheme and Value are the emsg scheme/value pair.  They are empty for
	// events carried by media samples.
	Scheme       string
	Value        string
	Data         []byte
	SourceOffset int64
	Seq          int
}

// Digest is the content digest of an event: SHA-256 over kind, scheme, value
// and the raw payload.  It is used both for de-duplication and for reports.
func (e Event) Digest() [32]byte {
	h := sha256.New()
	h.Write([]byte(e.Kind))
	h.Write([]byte{0})
	h.Write([]byte(e.Scheme))
	h.Write([]byte{0})
	h.Write([]byte(e.Value))
	h.Write([]byte{0})
	h.Write(e.Data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// DigestHex renders the content digest as lowercase hex.
func (e Event) DigestHex() string { d := e.Digest(); return hex.EncodeToString(d[:]) }

func less(a, b Event) bool {
	if c := a.Time.Cmp(b.Time); c != 0 {
		return c < 0
	}
	if a.TrackID != b.TrackID {
		return a.TrackID < b.TrackID
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	da, db := a.Digest(), b.Digest()
	if c := bytes.Compare(da[:], db[:]); c != 0 {
		return c < 0
	}
	return a.Seq < b.Seq
}

// Merged is the deterministic result of sorting and de-duplicating events.
type Merged struct {
	Events         []Event
	InputCount     int
	DuplicateCount int
	Timescale      uint32
}

// Merge places events on the least common multiple of their timescales (so
// every tick count is exact), sorts them deterministically and removes exact
// duplicates: same track, same rational time and same content digest.  Events
// that share track and time but carry different content are all kept.
func Merge(events []Event) (Merged, error) {
	res := Merged{InputCount: len(events)}
	if len(events) == 0 {
		return res, nil
	}
	timescales := make([]uint32, 0, len(events))
	for _, e := range events {
		if !e.Kind.Valid() {
			return Merged{}, fmt.Errorf("metadata: invalid event kind %q", e.Kind)
		}
		timescales = append(timescales, e.Time.Timescale)
	}
	ts, err := clock.Unify(timescales...)
	if err != nil {
		return Merged{}, fmt.Errorf("metadata: unify event timescales: %w", err)
	}
	res.Timescale = ts
	ordered := make([]Event, len(events))
	copy(ordered, events)
	for i := range ordered {
		ticks, exact, err := ordered[i].Time.Rescale(ts)
		if err != nil {
			return Merged{}, fmt.Errorf("metadata: rescale event time: %w", err)
		}
		if !exact {
			return Merged{}, fmt.Errorf("metadata: event time %s not exact on unified timescale %d", ordered[i].Time, ts)
		}
		ordered[i].Time = clock.New(ticks, ts)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return less(ordered[i], ordered[j]) })

	out := make([]Event, 0, len(ordered))
	for _, e := range ordered {
		if n := len(out); n > 0 {
			prev := out[n-1]
			if prev.Kind == e.Kind && prev.TrackID == e.TrackID && prev.Time.Cmp(e.Time) == 0 && prev.Digest() == e.Digest() {
				res.DuplicateCount++
				continue
			}
		}
		out = append(out, e)
	}
	res.Events = out
	return res, nil
}
