// Package metadata parses timed events from subtitles, chapter documents,
// MP4/CMAF boxes, H.264 SEI and RTP side-band packets. Every parser converts
// time through internal/timebase and reports malformed, regressing or
// out-of-range input instead of repairing it.
package metadata

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"replaypipe/internal/timebase"
)

// Kind classifies a timed event.
type Kind string

const (
	KindSubtitle Kind = "subtitle"
	KindChapter  Kind = "chapter"
	KindID3      Kind = "id3"
	KindEmsg     Kind = "emsg"
	KindSEI      Kind = "sei"
	KindRTP      Kind = "rtp"
)

// Event is one timed item normalised to nanoseconds on the shared time base.
// StartNS/EndNS are absolute media timeline positions. For instantaneous
// events EndNS equals StartNS. SourceOffset is the byte offset of the
// originating box, frame, cue or packet so review can jump back to the input.
type Event struct {
	ID           string `json:"id"`
	Kind         Kind   `json:"kind"`
	TrackID      uint32 `json:"track_id,omitempty"`
	Source       string `json:"source"`
	StartNS      int64  `json:"start_ns"`
	EndNS        int64  `json:"end_ns"`
	Timebase     string `json:"timebase"`
	Text         string `json:"text,omitempty"`
	SourceOffset int64  `json:"source_offset"`
}

// DurationNS is the event length, always non-negative.
func (e Event) DurationNS() int64 {
	if e.EndNS <= e.StartNS {
		return 0
	}
	return e.EndNS - e.StartNS
}

// Canonical returns a stable byte representation used by event digests and
// clip annotations. Source offsets are deliberately excluded: moving a box
// must not change event identity.
func (e Event) Canonical() []byte {
	return []byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%d\x00%s\x00%s",
		e.Kind, e.Source, e.TrackID, e.StartNS, e.EndNS, e.ID, e.Text))
}

// Digest returns the SHA-256 of the canonical event.
func (e Event) Digest() [32]byte { return sha256.Sum256(e.Canonical()) }

// DigestHex renders the event digest as lowercase hex.
func (e Event) DigestHex() string {
	d := e.Digest()
	return hex.EncodeToString(d[:])
}

// ParseOptions carries the media bounds and context shared by parsers.
type ParseOptions struct {
	MediaEndNS int64
	TrackID    uint32
	// AnchorNS is the sample/anchor presentation time for in-band formats
	// (ID3, SEI) that may omit an inline timestamp.
	AnchorNS int64
	// BaseOffset is added to parser-local byte offsets to obtain absolute
	// source offsets.
	BaseOffset int64
}

// Validate checks a single event against media bounds. It returns an
// EVENT_OUT_OF_RANGE error anchored at the event source offset.
func (e Event) Validate(opts ParseOptions) *timebase.Error {
	if e.StartNS < 0 || (opts.MediaEndNS > 0 && e.StartNS >= opts.MediaEndNS) {
		return timebase.Newf(timebase.CodeEventOutOfRange, e.TrackID, e.SourceOffset,
			"event %q start %d ns outside media [0,%d)", e.ID, e.StartNS, opts.MediaEndNS)
	}
	if e.EndNS < e.StartNS {
		return timebase.Newf(timebase.CodeTimeRegression, e.TrackID, e.SourceOffset,
			"event %q ends %d ns before start %d ns", e.ID, e.EndNS, e.StartNS)
	}
	if opts.MediaEndNS > 0 && e.EndNS > opts.MediaEndNS {
		return timebase.Newf(timebase.CodeEventOutOfRange, e.TrackID, e.SourceOffset,
			"event %q end %d ns beyond media end %d ns", e.ID, e.EndNS, opts.MediaEndNS)
	}
	return nil
}

// ValidateOrder verifies that events do not move backwards on the timeline.
// Events are checked in slice order, which parsers preserve from the source.
// Equal starts are allowed for simultaneous events.
func ValidateOrder(events []Event, opts ParseOptions) *timebase.Error {
	for i := 1; i < len(events); i++ {
		prev, cur := events[i-1], events[i]
		if cur.StartNS < prev.StartNS {
			return timebase.Newf(timebase.CodeTimeRegression, cur.TrackID, cur.SourceOffset,
				"event %q at %d ns precedes %q at %d ns", cur.ID, cur.StartNS, prev.ID, prev.StartNS)
		}
	}
	return nil
}

// SortStable orders events by start time then ID, preserving deterministic
// output independent of source enumeration order.
func SortStable(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].StartNS != events[j].StartNS {
			return events[i].StartNS < events[j].StartNS
		}
		return events[i].ID < events[j].ID
	})
}

// slug converts arbitrary text into a stable event id fragment.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "event"
	}
	return out
}

// firstOf returns the first non-blank string in order.
func firstOf(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
